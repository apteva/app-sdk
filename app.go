package sdk

import (
	"context"
	"database/sql"
	"net/http"
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
type Route struct {
	Method  string                                              // "" = any
	Pattern string                                              // mux-style, may include params
	Handler http.HandlerFunc
}

// Tool — one MCP tool exposed by the app. The framework wires these
// into a single MCP endpoint at the sidecar's /mcp.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any                                  // JSON schema
	Handler     ToolHandler
}

// ToolHandler is the per-call handler. ctx is the app context (DB,
// platform client, logger). The map is the raw arguments; return the
// MCP result as a JSON-encodable value.
type ToolHandler func(ctx *AppCtx, args map[string]any) (any, error)

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
type AppCtx struct {
	manifest *Manifest
	cfg      Config
	db       *sql.DB
	platform PlatformClient
	logger   Logger
	cancel   <-chan struct{}
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

// Done returns a channel that closes when the platform asks the
// sidecar to shut down. Long-running workers should select on it.
func (c *AppCtx) Done() <-chan struct{} { return c.cancel }

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
	AppName    string         `json:"app_name"`
	Version    string         `json:"version"`
	InstallID  int64          `json:"install_id"`
	ProjectID  string         `json:"project_id"`
	Permissions []Permission `json:"permissions"`
}
