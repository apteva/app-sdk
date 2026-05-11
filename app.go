package sdk

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// App is the single interface every Apteva app implements. Every facet
// is optional — return nil/zero to opt out. The platform calls methods
// in this order at boot:
//
//	Manifest  →  (server runs migrations)  →  OnMount  →  HTTPRoutes,
//	MCPTools, Channels, Workers, EventHandlers  (collected for mounting)
//
// Per-instance lifecycle hooks fire as instances bind/unbind to this
// app's installs at runtime, not at boot.
type App interface {
	Manifest() Manifest

	// OnMount runs once after the app DB is open + migrations have run.
	// Returning an error aborts the sidecar boot.
	OnMount(ctx *AppCtx) error

	// OnUnmount runs on graceful shutdown. Stop workers, flush state.
	OnUnmount(ctx *AppCtx) error

	HTTPRoutes() []Route
	MCPTools() []Tool
	Channels() []ChannelFactory
	Workers() []Worker
	EventHandlers() []EventHandler
}

// Route — single HTTP endpoint. The platform reverse-proxies to it
// under /apps/<name>/<pattern>.
//
// NoAuth opts the route out of the SDK's token-auth gate. Use it for
// endpoints that need to be reachable by parties that don't carry an
// APTEVA_APP_TOKEN — e.g. UPnP/DLNA wire endpoints accessed by smart
// TVs over the LAN, or any other "the LAN itself is the auth" model.
// The route handler becomes responsible for whatever access control
// is appropriate. Same exposure model as the existing /webhooks/*
// prefix carve-out, just opt-in per route.
type Route struct {
	Method  string                                              // "" = any
	Pattern string                                              // mux-style, may include params
	Handler http.HandlerFunc
	NoAuth  bool                                                // bypass withTokenAuth for this route
}

// Tool — one MCP tool exposed by the app. The framework wires these
// into a single MCP endpoint at the sidecar's /mcp.
//
// Set Handler for the original install-scoped signature; set
// HandlerCtx to receive a per-call context.Context carrying the
// Caller (instance id + grants). Exactly one of Handler / HandlerCtx
// must be set; HandlerCtx wins if both are.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any // JSON schema
	Handler     ToolHandler
	HandlerCtx  ToolHandlerCtx
}

// ToolHandler is the per-call handler. ctx is the app context (DB,
// platform client, logger). The map is the raw arguments; return the
// MCP result as a JSON-encodable value.
type ToolHandler func(ctx *AppCtx, args map[string]any) (any, error)

// ToolHandlerCtx is the per-call handler with a context.Context that
// carries the Caller — the calling agent's instance id + grants.
// Pull the Caller via sdk.CallerFrom(ctx). When the framework can't
// determine a Caller (header missing, older platform), CallerFrom
// returns nil and Caller methods treat that as full access.
type ToolHandlerCtx func(ctx context.Context, app *AppCtx, args map[string]any) (any, error)

// Worker — long-lived goroutine the framework supervises. Schedule is
// declarative ("@every 5m" / cron) for periodic workers; leave empty
// for one-shot run-to-completion workers.
type Worker struct {
	Name     string
	Schedule string
	Run      func(ctx context.Context, app *AppCtx) error
}

// EventHandler — subscription to a platform event topic. Platform pushes
// events to /apps/<name>/events; framework dispatches to handlers.
type EventHandler struct {
	Topic   string                                              // e.g. "instance.message", "connection.created"
	Handler func(ctx *AppCtx, event Event) error
}

type Event struct {
	Topic     string                 `json:"topic"`
	InstanceID int64                 `json:"instance_id,omitempty"`
	ProjectID  string                `json:"project_id,omitempty"`
	Data       map[string]any        `json:"data,omitempty"`
}

// ChannelFactory builds an inbound/outbound channel adapter for one
// install. The platform calls Build() with the install's config to
// obtain a Channel ready to receive/send. Channels are advanced —
// most apps won't use this.
type ChannelFactory interface {
	Name() string
	Build(ctx *AppCtx, config map[string]string) (Channel, error)
}

// Channel — symmetric duplex adapter. Send pushes outbound; the
// framework calls Receive when an inbound message arrives via the
// app's webhook route.
type Channel interface {
	Send(ctx context.Context, msg ChannelMessage) error
	Receive(ctx context.Context, msg ChannelMessage) error
}

type ChannelMessage struct {
	From string         `json:"from"`
	Text string         `json:"text"`
	Meta map[string]any `json:"meta,omitempty"`
}

// AppCtx is the only handle into the platform an app gets. Apps never
// import apteva-server directly — they call into AppCtx and the
// framework round-trips to the platform over HTTP using the app's
// short-lived APTEVA_APP_TOKEN.
//
// Project context. AppCtx carries an optional "current project" — the
// project the dispatch is running for. For project-scoped installs the
// SDK seeds it from APTEVA_PROJECT_ID at boot, so CurrentProject()
// returns that value across every worker tick / event handler / tool
// call. For global installs APTEVA_PROJECT_ID is empty; the SDK's
// worker dispatcher derives one AppCtx per project per tick via
// WithProject, so the worker body sees the project it's currently
// operating on. App code should always prefer ctx.CurrentProject()
// over reading the env directly — that's the single hook that makes
// a global install behave correctly.
type AppCtx struct {
	manifest       *Manifest
	cfg            Config
	db             *sql.DB
	platform       PlatformClient
	logger         Logger
	cancel         <-chan struct{}
	emitter        Emitter
	currentProject string // "" until WithProject is called or env seeds it
}

// AppDB is the app's private database handle, opened by the framework
// after migrations ran. Always non-nil if the manifest declares a db
// block; otherwise nil — the app should null-check.
func (c *AppCtx) AppDB() *sql.DB { return c.db }

// PlatformAPI returns a typed client for the small set of platform
// operations apps may legitimately need (read connection, send to
// channel, list instances). Every call is permission-checked server-
// side against the manifest's declared permissions.
func (c *AppCtx) PlatformAPI() PlatformClient { return c.platform }

// Manifest is the parsed apteva.yaml the app shipped — readable for
// runtime introspection (e.g. emitting a custom /version page).
func (c *AppCtx) Manifest() *Manifest { return c.manifest }

// Config is the user's filled-in install configuration (the
// config_schema fields). Decrypted by the platform before the app
// sees it.
func (c *AppCtx) Config() Config { return c.cfg }

// Logger is a structured logger pre-tagged with the app's name and
// install id.
func (c *AppCtx) Logger() Logger { return c.logger }

// DataDir returns the writable per-install directory the platform
// reserved for this app's persistent files (anything beyond AppDB —
// uploaded blobs, cloned repos, generated artifacts, …). Apps should
// route their on-disk state through this instead of hardcoding paths
// like "/data/foo": that works in container deployments where /data is
// a volume, but breaks every other host (macOS dev laptops, custom
// installer paths, anyone running apteva-server outside Docker).
//
// Resolution order:
//
//  1. APTEVA_DATA_DIR — preferred; the platform sets it to
//     <persistentRoot>/<install_id>/, the same dir the SDK opens
//     AppDB in.
//  2. dirname(DB_PATH) — graceful fallback for older platforms that
//     ship before APTEVA_DATA_DIR was introduced. Same physical
//     directory, just derived after the fact.
//  3. "" — neither is set; the manifest probably has no db block and
//     the platform spawned without setting APTEVA_DATA_DIR. Apps
//     needing a writable dir should treat this as a hard error and
//     refuse to mount, rather than silently picking a default that
//     won't exist.
func (c *AppCtx) DataDir() string {
	if v := os.Getenv("APTEVA_DATA_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("DB_PATH"); v != "" {
		return filepath.Dir(v)
	}
	return ""
}

// Done returns a channel that closes when the platform asks the
// sidecar to shut down. Long-running workers should select on it.
func (c *AppCtx) Done() <-chan struct{} { return c.cancel }

// CurrentProject returns the project the current dispatch is running
// for. Resolution order:
//
//  1. The value set via WithProject (worker tick, event handler, tool
//     call with _project_id resolved by the framework)
//  2. APTEVA_PROJECT_ID env (project-scoped installs)
//  3. "" — global install with no per-call project context
//
// App code should call this in place of os.Getenv("APTEVA_PROJECT_ID")
// — it's the single API that makes global installs behave correctly.
// A returned "" means "the framework didn't dispatch this call against
// any project"; for global apps that's a bug to flag, not silently
// no-op.
func (c *AppCtx) CurrentProject() string {
	if c == nil {
		return ""
	}
	if c.currentProject != "" {
		return c.currentProject
	}
	return os.Getenv("APTEVA_PROJECT_ID")
}

// WithProject returns an AppCtx whose CurrentProject() returns the
// given project id, leaving every other field shared with the parent.
// Use it to pin a project on cross-call boundaries (e.g. the SDK's
// worker dispatcher does this once per project per tick). The
// PlatformClient handle is wrapped so subsequent CallApp / CallAppResult
// invocations auto-thread `_project_id` into the downstream args —
// global apps' workers don't have to thread project context manually.
//
// Passing "" reverts to env-driven behaviour.
func (c *AppCtx) WithProject(projectID string) *AppCtx {
	if c == nil {
		return nil
	}
	cp := *c
	cp.currentProject = projectID
	cp.platform = wrapPlatformWithProject(c.platform, projectID)
	return &cp
}

// IntegrationFor returns the binding for a role declared in the
// manifest's requires.integrations. Returns nil when:
//
//   - the role isn't declared in the manifest (caller bug)
//   - the role is declared but the operator didn't bind it (optional
//     dep skipped, or required dep install in flight)
//   - the bound target was uninstalled (status=degraded)
//
// Apps should null-check on every call and degrade gracefully when
// nil — that's the whole point of optional deps. Required deps
// being nil means the install is misconfigured; surface a clean
// error to the agent rather than crashing.
//
// The lookup hits the platform's /whoami once per AppCtx and caches
// the bindings for the process lifetime. Since bindings can change
// at runtime (operator rebinds, optional dep newly available), the
// cache TTL is short — sub-second — so the next call after a
// rebind picks up the change without a sidecar restart.
func (c *AppCtx) IntegrationFor(role string) *BoundIntegration {
	if c == nil || c.platform == nil {
		return nil
	}
	bindings, manifest := c.bindingsAndManifest()
	if manifest == nil {
		return nil
	}
	var dep *IntegrationDep
	for i := range manifest.Requires.Integrations {
		if manifest.Requires.Integrations[i].Role == role {
			dep = &manifest.Requires.Integrations[i]
			break
		}
	}
	if dep == nil {
		return nil
	}
	raw, ok := bindings[role]
	if !ok || raw == nil {
		return nil
	}
	bound := &BoundIntegration{
		Role: role,
		Kind: dep.Kind,
	}
	if bound.Kind == "" {
		bound.Kind = "integration"
	}
	switch v := raw.(type) {
	case float64:
		if v <= 0 {
			return nil
		}
		if bound.Kind == "app" {
			bound.InstallID = int64(v)
			// AppName resolution: best-effort GetInstance lookup.
			// Falls through with empty AppName when the platform
			// doesn't have a fast path; consumers fall back to the
			// install id, which is what authorization gates on
			// anyway.
			if inst, err := c.platform.GetInstance(int64(v)); err == nil && inst != nil {
				bound.AppName = inst.Name
			}
			// kind=app deps usually call CallApp(appName) — and
			// the bound app's display name is more useful than its
			// numeric id for that. Most apps in practice have a
			// stable known name (storage, media, etc.) and the
			// caller passes the literal string anyway.
		} else {
			bound.ConnectionID = int64(v)
			// Resolve the connection's app_slug so app code can do
			// provider-specific normalization without a separate
			// GetConnection round-trip. This is one /api/apps/callback/
			// connections/:id call per IntegrationFor invocation;
			// ToolFor uses it to map logical capabilities to upstream
			// tool names. Best-effort — leave AppSlug empty on error
			// and let app code fall back to its own defaults.
			if conn, err := c.platform.GetConnection(int64(v)); err == nil && conn != nil {
				bound.AppSlug = conn.AppSlug
			}
		}
	default:
		return nil
	}
	// Build ToolFor closure that maps logical capability → upstream tool name
	// per the manifest's tools map.
	tools := dep.Tools
	bound.ToolFor = func(capability string) string {
		if tools == nil {
			return capability
		}
		if t, ok := tools[capability]; ok {
			return t
		}
		return capability
	}
	return bound
}

// BoundIntegration describes a role's currently-bound target. See
// AppCtx.IntegrationFor.
type BoundIntegration struct {
	Role         string
	Kind         string // "integration" | "app"
	ConnectionID int64  // when Kind=integration
	InstallID    int64  // when Kind=app
	AppName      string // when Kind=app — resolved best-effort
	AppSlug      string // when Kind=integration — fetched on demand from PlatformAPI.GetConnection
	// ToolFor maps a logical capability ("image.generate") to the
	// upstream tool name ("generate_image" for openai-api). Falls
	// back to the capability string when no mapping is declared.
	ToolFor func(capability string) string
}

// bindingsAndManifest fetches the install's bindings + manifest. The
// httpPlatformClient caches both for the AppCtx's lifetime; tests
// can stub via a custom PlatformClient.
func (c *AppCtx) bindingsAndManifest() (map[string]any, *Manifest) {
	if c.manifest == nil || c.platform == nil {
		return nil, c.manifest
	}
	// httpPlatformClient.WhoAmI returns identity + bindings. Pull
	// fresh on every call — the cache + revalidation lives in the
	// platform client itself, not here.
	id, err := c.platform.WhoAmI()
	if err != nil || id == nil {
		return nil, c.manifest
	}
	return id.Bindings, c.manifest
}

// Emit publishes an event onto the platform's app-event bus. Topic is
// app-relative (e.g. "file.added") — the platform stamps the app
// prefix from the install token before fanning out. Data must be
// JSON-encodable; pass nil if the topic alone is enough signal.
//
// The event is tagged with c.CurrentProject() as its project_id —
// so emits from a per-request-scoped ctx (tool calls, event handlers,
// worker ticks) automatically carry the right project even when the
// install is global. Apps that need to emit on behalf of a different
// project (e.g. storage processes a file in project X from a global
// install's HTTP handler) should use EmitWithProject directly.
//
// Fire-and-forget: a 100ms timeout caps the publish, errors are
// logged but never bubbled to the caller. UI fanout is best-effort
// and the app's own DB is the source of truth — a missed event is a
// reconnect-with-since= away from being recovered by the dashboard.
//
// Safe to call from any goroutine. Safe to call when no subscribers
// are connected (the platform's ring buffer holds the last 256
// events per (app, project) for fast reconnect replay).
func (c *AppCtx) Emit(topic string, data any) {
	if c == nil || c.emitter == nil {
		return
	}
	c.emitter.EmitWithProject(topic, c.CurrentProject(), data)
}

// EmitWithProject publishes an event tagged with an explicit
// project_id, ignoring c.CurrentProject(). Use this when the project
// the event belongs to is a property of the *data*, not the dispatch
// context — e.g. storage's file event handlers run from an
// unscoped ctx (HTTP route) but the file's project_id is on the row.
//
// For project-scoped installs the platform enforces the install's
// pinned project on the server side, so a project_id argument that
// disagrees is silently overridden (you can't spoof outside your
// scope). Global installs may emit for any project the install's
// owner has access to.
//
// Passing projectID="" emits a project-less event — lands on the
// wildcard lane only. Use sparingly; most domain events belong to
// a project.
func (c *AppCtx) EmitWithProject(topic, projectID string, data any) {
	if c == nil || c.emitter == nil {
		return
	}
	c.emitter.EmitWithProject(topic, projectID, data)
}

// Emitter is the indirection between AppCtx and the HTTP-based emit
// implementation in run.go. Exposed so app-sdk/testkit can stub it
// with an in-memory recorder for assertions.
//
// EmitWithProject carries an explicit project_id on the wire. The
// HTTP transport encodes it into the emit body so the platform
// stamps the (app, project) lane correctly even when the install is
// global. Passing projectID="" yields a project-less event.
type Emitter interface {
	EmitWithProject(topic, projectID string, data any)
}

// SetEmitter swaps the AppCtx's event emitter — only valid for
// test-built contexts (NewAppCtxForTest). Production code never
// needs this; the framework wires the HTTP emitter automatically.
// Unexported field access through this method keeps the testkit
// from reaching into unexported state directly.
func (c *AppCtx) SetEmitter(e Emitter) {
	if c == nil {
		return
	}
	c.emitter = e
}

// NewAppCtxForTest constructs an *AppCtx for use by the testkit
// package and its callers. Production code never needs this — the
// framework builds AppCtx in Run(). Exposed so app-sdk/testkit can
// hand-craft a context backed by an in-memory database, with no
// platform client and a no-op logger.
//
// Pass nil for any of: manifest (gets a zero-value pointer),
// platform (tests that don't call platform), logger (uses the
// silent default). cfg may be nil — becomes empty Config.
func NewAppCtxForTest(manifest *Manifest, db *sql.DB, cfg Config, platform PlatformClient, logger Logger) *AppCtx {
	if manifest == nil {
		manifest = &Manifest{}
	}
	if cfg == nil {
		cfg = Config{}
	}
	if logger == nil {
		logger = silentLogger{}
	}
	return &AppCtx{
		manifest: manifest,
		cfg:      cfg,
		db:       db,
		platform: platform,
		logger:   logger,
		cancel:   make(chan struct{}),
	}
}

// silentLogger drops every message — used as the test default so
// `go test -v` output isn't drowned in app boot logs.
type silentLogger struct{}

func (silentLogger) Info(string, ...any)  {}
func (silentLogger) Warn(string, ...any)  {}
func (silentLogger) Error(string, ...any) {}

// Config — typed access to user-supplied install configuration.
type Config map[string]string

func (c Config) Get(name string) string  { return c[name] }
func (c Config) Has(name string) bool    { _, ok := c[name]; return ok }

// Logger — minimal structured logging interface so apps don't have to
// import a logger package. Framework provides the default impl.
type Logger interface {
	Info(msg string, keyvals ...any)
	Warn(msg string, keyvals ...any)
	Error(msg string, keyvals ...any)
}

// PlatformClient — the small RPC surface apps may use to interact
// with the platform. Each method is permission-gated; calling without
// the matching declared permission returns an error 403.
type PlatformClient interface {
	// Connections
	GetConnection(id int64) (*PlatformConnection, error)
	ListConnections(filter ConnectionFilter) ([]PlatformConnection, error)

	// Instances
	GetInstance(id int64) (*PlatformInstance, error)
	SendEvent(instanceID int64, message string) error

	// Channels
	SendToChannel(channelName, projectID, message string) error

	// Self
	WhoAmI() (*InstallIdentity, error)

	// Integration execution. Calls a tool on a connection bound to
	// this install at install time. Credentials never leave the
	// platform; the response shape mirrors /connections/:id/execute.
	// Authorization: the install must declare
	// platform.connections.execute and connID must be in the
	// install's integration_bindings.
	ExecuteIntegrationTool(connID int64, tool string, input map[string]any) (*ExecuteResult, error)

	// App-to-app call. Invokes an MCP tool on a sibling app the
	// install was bound to. Authorization: the install must declare
	// platform.apps.call and appName must be in a kind=app binding.
	//
	// Returns the raw JSON-RPC envelope from the target sidecar's
	// /mcp endpoint:
	//
	//   {"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text",
	//    "text":"<tool's actual JSON return>"}]}}
	//
	// Callers that just want the tool's return shape should prefer
	// CallAppResult (added in v0.1.8) which unwraps that envelope
	// for them. CallApp stays for callers that already unwrap
	// themselves and for non-tool /mcp methods (resources/list, etc.).
	CallApp(appName, tool string, input map[string]any) (json.RawMessage, error)

	// CallAppResult is CallApp + automatic JSON-RPC envelope unwrap.
	// Decodes result.content[0].text directly into out so callers
	// can use the tool's natural shape:
	//
	//   var got struct{ Files []File `json:"files"` }
	//   if err := api.CallAppResult("storage", "files_list", args, &got); err != nil {
	//     return err
	//   }
	//   // got.Files is the tool's `files` array.
	//
	// Falls back to decoding the raw response when the envelope is
	// missing — covers the "the platform short-circuited an
	// unwrapped response" path some test/mocked clients use.
	CallAppResult(appName, tool string, input map[string]any, out any) error

	// App-initiated OAuth. Creates a pending connection owned by this
	// install (created_via=app_install, owner_app_install_id=<this>),
	// returns the authorize URL the app should hand the user, and the
	// connection id to poll. After the human completes the dance,
	// apteva-server 302s the browser back to ReturnURL with
	// ?conn_id=<id>&status=ok. Authorization: install must declare
	// platform.oauth.start.
	StartOAuth(req OAuthStartRequest) (*OAuthStartResult, error)

	// Disconnect a connection this install owns. Authorization: install
	// must declare platform.connections.manage AND the connection must
	// have owner_app_install_id matching this install (apps cannot
	// touch operator-managed connections or other apps' connections).
	DisconnectConnection(connID int64) error

	// List connections owned by this install. Returns only rows where
	// owner_app_install_id matches; never operator rows. Authorization:
	// install must declare platform.connections.read or .manage.
	ListOwnedConnections() ([]PlatformConnection, error)

	// GetGrants returns the per-(this install, instanceID) policy the
	// operator wrote for the calling agent. Used by the SDK's MCP
	// handler to gate tool calls. Returns a zero-value response (no
	// rules, default allow) when the platform doesn't yet implement
	// the endpoint — back-compat with older servers.
	GetGrants(instanceID int64) (*GrantsResponse, error)

	// GetConnectionCredentials returns the decrypted credentials for a
	// bound connection. Use this when the integration runner's
	// tool-call surface isn't expressive enough — multipart uploads,
	// presigned URLs, range GETs against an S3-compatible backend, etc.
	//
	// Requirements (all enforced server-side):
	//   1. Manifest declares platform.connections.read_credentials.
	//   2. The connection's slug is in some requires.integrations[].
	//      compatible_slugs entry on this install's manifest.
	//   3. The connection ID is in the install's integration_bindings
	//      for one of those roles (operator actually bound it).
	GetConnectionCredentials(id int64) (*ConnectionCredentials, error)

	// ListProjects returns the projects this install can dispatch
	// against. For project-scoped installs that's a singleton list
	// holding the install's pinned project. For global installs it's
	// every project the install's owner has access to. The SDK's
	// worker loop calls this once per tick to fan workers out per
	// project; apps that hand-roll dispatch (custom polling, custom
	// SSE subscriptions) can call it too.
	//
	// Authorization: no manifest permission required — every install
	// is allowed to enumerate its own projection. The list is filtered
	// server-side by ownership.
	ListProjects() ([]PlatformProject, error)
}

// PlatformProject is the minimal project descriptor PlatformClient.ListProjects
// returns. Apps that need richer per-project metadata (description,
// settings, …) should query WhoAmI per project, but for the common
// "loop over my projects and call storage once per project" use case
// ID + Name is enough.
type PlatformProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// GrantsResponse is what GetGrants returns. DefaultEffect is the
// install-wide fallback when no rule matches; Grants is the rule list
// for this (install, instance) pair. Empty rules + default "allow" =
// "this agent has full access" — the back-compat default.
type GrantsResponse struct {
	DefaultEffect string  `json:"default_effect"`
	Grants        []Grant `json:"grants"`
}

// OAuthStartRequest is the body for PlatformClient.StartOAuth.
type OAuthStartRequest struct {
	// IntegrationSlug — catalog slug ("twitter-api", "facebook-graph").
	IntegrationSlug string `json:"integration_slug"`
	// ReturnURL — where the platform 302s the browser after the OAuth
	// callback completes. Must be on the same host as the dashboard;
	// usually a path under the app's own /api/apps/<name>/... surface.
	ReturnURL string `json:"return_url"`
	// Name — a display label for the connection (e.g. "Twitter for
	// @marcoschwartz"). Optional; falls back to the integration name.
	Name string `json:"name,omitempty"`
	// ProjectID — scope. Defaults to the install's project when empty.
	ProjectID string `json:"project_id,omitempty"`
}

// OAuthStartResult is the response from PlatformClient.StartOAuth.
type OAuthStartResult struct {
	ConnectionID int64  `json:"connection_id"`
	AuthorizeURL string `json:"authorize_url"`
	ExpiresAt    string `json:"expires_at"`
}

// ExecuteResult mirrors apteva-server's response shape for integration
// tool executions. data is the upstream API's response body parsed
// from JSON; status is the HTTP status the upstream returned.
//
// Headers carries a small allowlisted set of response headers the
// server forwards from the upstream (Location, Content-Type, Etag,
// Last-Modified). Apps that need to follow a redirect-style flow
// (YouTube resumable upload init returns the session URL only via
// Location) can read it here. Empty when the server is older than
// the introduction of header forwarding.
type ExecuteResult struct {
	Success bool              `json:"success"`
	Status  int               `json:"status"`
	Data    json.RawMessage   `json:"data"`
	Headers map[string]string `json:"headers,omitempty"`
}

// ConnectionCredentials is the decrypted credential bundle for a
// connection this install has bound. Returned by
// PlatformClient.GetConnectionCredentials. Fields keys mirror the
// catalog entry's auth.credential_fields[].name — slug-specific
// endpoint construction is the caller's job.
type ConnectionCredentials struct {
	ConnectionID int64             `json:"id"`
	Slug         string            `json:"slug"`
	Fields       map[string]string `json:"fields"`
	FetchedAt    time.Time         `json:"fetched_at"`
}

type PlatformConnection struct {
	ID         int64  `json:"id"`
	AppSlug    string `json:"app_slug"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	ProjectID  string `json:"project_id"`
}

type ConnectionFilter struct {
	ProjectID string
	AppSlug   string
}

type PlatformInstance struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Mode      string `json:"mode"`
	ProjectID string `json:"project_id"`
}

// InstallIdentity — what the app is and where it sits, returned by
// WhoAmI(). Useful when an app wants to log its own install id without
// re-reading env vars.
type InstallIdentity struct {
	AppName     string         `json:"app_name"`
	Version     string         `json:"version"`
	InstallID   int64          `json:"install_id"`
	ProjectID   string         `json:"project_id"`
	// ProjectName is the operator-set human label for the project
	// (Settings → Projects → Name). Empty for global installs.
	// Apps that surface human-readable references to the project
	// (LLM system prompts, panel headers, generated documents)
	// should use this rather than the opaque ProjectID.
	ProjectName string `json:"project_name,omitempty"`
	// ProjectDescription is the operator-set free-text description
	// for the project (Settings → Projects → Description). Empty
	// when the operator hasn't filled it in. Useful as context for
	// LLM-using apps — e.g. media's describer prepends it to the
	// system prompt so generated descriptions land in the right
	// register ("internal team standups", "cooking show clips").
	ProjectDescription string `json:"project_description,omitempty"`
	Permissions []Permission   `json:"permissions"`
	// Bindings carries the install's integration_bindings JSON —
	// keys are role names declared in the manifest, values are
	// connection_id (kind=integration) or install_id (kind=app),
	// or null when the operator declined an optional dep.
	Bindings map[string]any `json:"bindings"`
	// PublicURL is the platform's externally-reachable base URL
	// (e.g. "https://agents.example.com") — admin-editable from
	// Settings → Server, falls back to the PUBLIC_URL env var, falls
	// back to "" when unset (dev/local installs). Apps that mint
	// shareable links should prepend this rather than reading
	// APTEVA_PUBLIC_URL directly so settings changes propagate
	// through WhoAmI's sub-second cache without a sidecar restart.
	// Empty string means "no absolute URL configured" — apps should
	// fall back to relative paths and document the limitation.
	PublicURL string `json:"public_url,omitempty"`
}
