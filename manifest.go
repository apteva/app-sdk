// Package sdk is the public Go SDK for Apteva Apps. Apps depend only
// on this module — never on apteva-server internals — so the contract
// stays narrow and apps stay independently shippable.
//
// Schema versioning: every breaking change to Manifest bumps Schema.
// Apps validate at boot via ValidateManifest; the server validates at
// install time. Unknown fields fail closed.
package sdk

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SchemaCurrent is the manifest schema version this SDK understands.
// Bumped only on breaking changes; additive fields don't bump it.
const SchemaCurrent = "apteva-app/v1"

// Manifest is the single source of truth for an app — identity,
// requirements, what it provides, and how the platform should host it.
// Loaded from apteva.yaml in the app's repo root.
type Manifest struct {
	Schema      string   `yaml:"schema" json:"schema"`
	Name        string   `yaml:"name" json:"name"` // slug; matches repo and orchestrator service
	DisplayName string   `yaml:"display_name" json:"display_name"`
	Version     string   `yaml:"version" json:"version"` // semver
	Description string   `yaml:"description" json:"description"`
	Author      string   `yaml:"author" json:"author"`
	Homepage    string   `yaml:"homepage" json:"homepage"`
	Icon        string   `yaml:"icon" json:"icon"`
	Tags        []string `yaml:"tags" json:"tags"`

	Scopes           []Scope `yaml:"scopes" json:"scopes"`
	MinAptevaVersion string  `yaml:"min_apteva_version" json:"min_apteva_version"`

	Requires Requires `yaml:"requires" json:"requires"`
	Provides Provides `yaml:"provides" json:"provides"`

	Runtime      Runtime       `yaml:"runtime" json:"runtime"`
	DB           *DBConfig     `yaml:"db,omitempty" json:"db,omitempty"`
	ConfigSchema []ConfigField `yaml:"config_schema" json:"config_schema"`

	// Imports is an app-owned declarative catalog of external sources this
	// app knows how to import into itself. The platform may interpret this
	// in a future import/sync runner; apps that don't declare imports omit it.
	Imports map[string]any `yaml:"imports,omitempty" json:"imports,omitempty"`

	UpgradePolicy UpgradePolicy `yaml:"upgrade_policy" json:"upgrade_policy"`
}

// Scope determines whether an install is project-local or server-global.
type Scope string

const (
	ScopeProject Scope = "project"
	ScopeGlobal  Scope = "global"
)

// Requires lists what the app needs from the platform — permissions,
// MCP tools the user must attach, minimum platform version, and
// other Apteva apps that must be installed alongside.
type Requires struct {
	Permissions       []Permission     `yaml:"permissions" json:"permissions"`
	MCPToolsAtRuntime []string         `yaml:"mcp_tools_at_runtime" json:"mcp_tools_at_runtime"`
	Apps              []RequiredAppRef `yaml:"apps,omitempty" json:"apps,omitempty"`
	// Integrations declares roles this app fills with either an
	// integration connection or another Apteva app. The operator
	// binds each role at install time; the app reads the binding
	// at runtime via ctx.IntegrationFor(role) and never sees raw
	// credentials. See IntegrationDep below.
	Integrations []IntegrationDep `yaml:"integrations,omitempty" json:"integrations,omitempty"`
	// Binaries declares native executables the app needs at runtime
	// (ffmpeg, ffprobe, …). The platform downloads + extracts each
	// dep into a shared cache (~/.apteva/binaries/<name>-<version>-<os>-<arch>/)
	// and prepends that directory to the sidecar's PATH, so the
	// app's existing exec.LookPath / exec.Command calls resolve to
	// the bundled binary. Apps without a binaries block keep relying
	// on the host's PATH as before.
	Binaries []BinaryDep `yaml:"binaries,omitempty" json:"binaries,omitempty"`

	// DynamicAppCalls opts the caller into runtime-resolved cross-app
	// calls: handler code calling ctx.PlatformAPI().CallAppResult(app,
	// tool, input) against any installed app, not just statically-
	// declared ones in Apps. Honoured by apteva-server only for
	// callers identified as official (manifest.runtime.source.repo
	// prefix match — default github.com/apteva/, extendable via
	// APTEVA_OFFICIAL_APP_PREFIXES). Without that match, this flag is
	// a no-op — third-party apps that try to set it gain nothing.
	//
	// Defined for generic-FaaS-style apps (functions, future workflow
	// runners) whose call targets aren't knowable at install time.
	// The proper per-call permission model is a v2 follow-on.
	DynamicAppCalls bool `yaml:"dynamic_app_calls,omitempty" json:"dynamic_app_calls,omitempty"`

	// DynamicIntegrationAccess is the sibling capability for reaching
	// integration connections (Pushover, Slack, Resend, ...) by raw
	// connection_id at runtime, without the operator pre-binding each
	// role per Integrations. Same trust model as DynamicAppCalls:
	// gated by apteva-server on isOfficialCaller, and project-scoped
	// — the resolved connection's project_id must match the caller
	// install's. Defined for generic runners (workflows, functions)
	// whose target connections are user-supplied at workflow-create /
	// deploy time.
	DynamicIntegrationAccess bool `yaml:"dynamic_integration_access,omitempty" json:"dynamic_integration_access,omitempty"`
}

// BinaryDep declares one native executable (or a set of related
// executables shipped together in the same archive — e.g. ffmpeg +
// ffprobe) that the platform materializes on the host before
// spawning the sidecar.
//
// Version is pinned: no "latest". The trust anchor is the SHA256 in
// each BinarySource; the manifest lives in the app's git repo, which
// the platform already trusts (it builds the app from there).
type BinaryDep struct {
	Name        string   `yaml:"name" json:"name"`
	Version     string   `yaml:"version" json:"version"`
	Executables []string `yaml:"executables,omitempty" json:"executables,omitempty"`
	// Sources maps "<os>-<arch>" (e.g. "linux-amd64") to a per-platform
	// download descriptor. A missing entry for the runtime platform
	// causes install to fail when Required=true, or silently skip the
	// dep when Required=false.
	Sources  map[string]BinarySource `yaml:"sources" json:"sources"`
	Required bool                    `yaml:"required,omitempty" json:"required,omitempty"`
	Hint     string                  `yaml:"hint,omitempty" json:"hint,omitempty"`
}

// BinarySource is one platform's download descriptor.
//
//   - Archive selects the unpacker: "tar.xz" | "tar.gz" | "zip" |
//     "raw" (body is the binary itself).
//   - StripRoot peels N top-level directory components off the
//     extracted tree, mirroring tar's --strip-components.
type BinarySource struct {
	URL       string `yaml:"url" json:"url"`
	SHA256    string `yaml:"sha256" json:"sha256"`
	Archive   string `yaml:"archive,omitempty" json:"archive,omitempty"`
	StripRoot int    `yaml:"strip_root,omitempty" json:"strip_root,omitempty"`
}

// IntegrationDep declares one role the app needs filled. Two kinds:
//
//	kind: integration  — bind a connection (per-project credentials
//	                     for some upstream like OpenAI). The platform
//	                     executes tools server-side via the existing
//	                     integration runner; the app never holds the
//	                     secret.
//
//	kind: app          — bind another Apteva app installed in the
//	                     same project. The platform proxies MCP calls
//	                     from this app to the target sidecar; auth is
//	                     the binding itself.
//
// Required deps block install until bound. Optional deps surface as
// opt-in checkboxes; the app degrades gracefully when the role is
// unbound (ctx.IntegrationFor returns nil). Late binding is supported:
// when a compatible target appears later, the install's UI prompts
// the operator to wire it up — no app restart needed.
type IntegrationDep struct {
	// Role is app-defined; doesn't have to match anything upstream.
	// It's the key the app reads via ctx.IntegrationFor("role").
	Role string `yaml:"role" json:"role"`
	// Kind selects the resolution path. Default "integration".
	Kind string `yaml:"kind,omitempty" json:"kind,omitempty"` // "integration" | "app"
	// Required=true blocks the install until bound. False makes
	// it an opt-in.
	Required bool `yaml:"required,omitempty" json:"required,omitempty"`
	// CompatibleSlugs lists upstream integration slugs (matching
	// integrations/src/apps/<slug>.json) that can fill this role.
	// Used when kind=integration. The install picker filters to
	// connections whose app_slug is in this list.
	CompatibleSlugs []string `yaml:"compatible_slugs,omitempty" json:"compatible_slugs,omitempty"`
	// CompatibleAppNames lists the apps that can fill this role
	// when kind=app. The install picker filters to running installs
	// whose app name is in this list.
	CompatibleAppNames []string `yaml:"compatible_app_names,omitempty" json:"compatible_app_names,omitempty"`
	// Capabilities is informational — describes the abstract things
	// this role does, e.g. ["image.generate", "image.edit"]. Future
	// versions will use this for capability-based matching across
	// providers; v0.1 just renders it in the install picker so the
	// operator knows what they're signing up for.
	Capabilities []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	// Tools maps logical capability → upstream tool name for this
	// role. Lets the same app code work across providers without
	// branching on app_slug. e.g. for image.generate, openai-api
	// uses "generate_image" and replicate uses "predictions_create".
	Tools map[string]string `yaml:"tools,omitempty" json:"tools,omitempty"`
	// Hint is shown in the install picker when no compatible target
	// exists yet, nudging the operator to install one.
	Hint string `yaml:"hint,omitempty" json:"hint,omitempty"`
	// Label is the human-readable role name for the install UI.
	// Falls back to Role when empty.
	Label string `yaml:"label,omitempty" json:"label,omitempty"`
}

// RequiredAppRef declares a dependency on another Apteva app. The
// platform's install flow uses this to install missing deps in topo
// order before this app, and the uninstall flow uses it to refuse
// removing an app another app hard-depends on. Optional deps
// degrade gracefully — the dependent's UI hides surfaces tied to
// the missing app instead of failing.
type RequiredAppRef struct {
	Name     string `yaml:"name" json:"name"`                           // matches the dep's manifest.name
	Version  string `yaml:"version,omitempty" json:"version,omitempty"` // semver constraint, ">=1.0.0" form; empty = any
	Reason   string `yaml:"reason,omitempty" json:"reason,omitempty"`   // human-readable; surfaced in the dashboard
	Optional bool   `yaml:"optional,omitempty" json:"optional,omitempty"`
}

// Provides describes the surfaces this app contributes back to the
// platform — none, one, or many.
type Provides struct {
	HTTPRoutes      []RouteSpec      `yaml:"http_routes" json:"http_routes"`
	MCPTools        []MCPToolSpec    `yaml:"mcp_tools" json:"mcp_tools"`
	PromptFragments []PromptFragment `yaml:"prompt_fragments" json:"prompt_fragments"`
	UIPanels        []UIPanel        `yaml:"ui_panels" json:"ui_panels"`
	UIComponents    []UIComponent    `yaml:"ui_components,omitempty" json:"ui_components,omitempty"`
	UIApp           *UIApp           `yaml:"ui_app,omitempty" json:"ui_app,omitempty"`
	Channels        []ChannelSpec    `yaml:"channels" json:"channels"`
	Workers         []WorkerSpec     `yaml:"workers" json:"workers"`
	// Skills the app ships — markdown-bodied playbooks the agent
	// loads on demand to act with this app's expertise. Each entry
	// becomes one row in the platform's skills table on install,
	// refreshes on upgrade, cascade-deletes on uninstall.
	Skills []Skill `yaml:"skills,omitempty" json:"skills,omitempty"`

	// Resources + ProvidedPermissions opt the app into per-(install,
	// instance) authorization. Apps that omit both keep today's
	// "every bound agent has full access" behavior. See ResourceDecl
	// and ProvidedPermission for the shape, and the Caller type in
	// app.go for runtime enforcement.
	Resources           []ResourceDecl       `yaml:"resources,omitempty" json:"resources,omitempty"`
	ProvidedPermissions []ProvidedPermission `yaml:"permissions,omitempty" json:"permissions,omitempty"`

	// Publishes is the declarative list of topics this app emits on
	// the platform's AppBus (via ctx.Emit at runtime). Two consumers
	// today: (1) the dashboard's subscription form renders a curated
	// dropdown instead of a free-text topic input, and (2) the
	// agent's directive picks up the catalog when reasoning about
	// "what events can I subscribe to". Apps that emit nothing or
	// haven't documented their emissions yet omit this and the
	// dashboard falls back to free-text.
	Publishes []EventDecl `yaml:"publishes,omitempty" json:"publishes,omitempty"`
}

// EventDecl describes one topic the app emits on the AppBus. Pure
// declaration — the platform does NOT enforce that runtime emits
// match the declared schema (apps emit through ctx.Emit which only
// cares about the topic string). The dashboard uses these for the
// subscription form's event picker and for tooltip descriptions.
type EventDecl struct {
	// Name is the topic string, dot-separated by convention
	// ("media.indexed", "account.created"). Must match exactly what
	// ctx.Emit("…", …) passes at runtime.
	Name string `yaml:"name" json:"name"`
	// Description is a one-line human-readable explanation of what
	// the event represents. Surfaced as the dropdown option's
	// tooltip / sub-line in the dashboard.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Dynamic indicates that the topic has a runtime-synthesized
	// suffix (e.g. mqtt's "mqtt.<broker-topic>" or streaming's
	// "stream.<id>.viewer_count_changed"). When true, Name must end
	// with ".*" so the dashboard can render the option with the
	// trailing wildcard explicit. Subscribers see the literal
	// pattern.
	Dynamic bool `yaml:"dynamic,omitempty" json:"dynamic,omitempty"`
	// Payload is documentation-only: a loose map of field-name → a
	// type label ("string", "integer", "boolean", "object", "array",
	// or richer ASCII prose). Not validated; never enforced.
	// Surfaced in the dashboard tooltip so an operator authoring
	// a subscription knows what they'll receive.
	Payload map[string]string `yaml:"payload,omitempty" json:"payload,omitempty"`
}

// ResourceDecl describes one shape of resource the app exposes to
// callers — a folder, a social account, a project, a tag set. The
// platform uses this to render an appropriate picker when an operator
// is writing grants, and the runtime uses it to interpret the
// `resource` string on each grant rule.
//
// Empty Resources slice = no resource-shaped scoping (permissions are
// boolean only).
type ResourceDecl struct {
	// Name is the type id referenced by ProvidedPermission.Resource.
	Name string `yaml:"name" json:"name"`
	// Label is the human-readable name shown by the dashboard.
	Label string `yaml:"label,omitempty" json:"label,omitempty"`
	// ListEndpoint is the app-relative path the dashboard hits to
	// populate the picker (returns {items: [{id,label,parent?}]}).
	// Empty = no picker; the dashboard renders a freeform field.
	ListEndpoint string `yaml:"list_endpoint,omitempty" json:"list_endpoint,omitempty"`
	// Matcher selects how grant.resource is compared against runtime
	// resources. One of: glob, id_set, prefix, tag_set, exact.
	Matcher string `yaml:"matcher" json:"matcher"`
	// Picker hints the dashboard rendering. One of: tree, list,
	// search, tags, freeform. Defaults to freeform when empty.
	Picker string `yaml:"picker,omitempty" json:"picker,omitempty"`
	// ListingVisibility controls how lists/searches behave when the
	// caller is partially scoped. Tree-shaped resources default to
	// "navigable" (ancestor stubs visible so the caller can drill in
	// to the allowed subtree). Other shapes default to "scoped_only".
	ListingVisibility string `yaml:"listing_visibility,omitempty" json:"listing_visibility,omitempty"`
}

// ProvidedPermission is one verb the app exposes — read, write,
// delete, publish, etc. Operators write grants in terms of these.
type ProvidedPermission struct {
	// Name is the permission id (dotted, app-scoped — "files.read",
	// "posts.publish"). Referenced from MCPToolSpec.Requires.
	Name string `yaml:"name" json:"name"`
	// Resource references one of Provides.Resources by name.
	// Empty = unparameterized (boolean grant; resource pattern
	// ignored).
	Resource string `yaml:"resource,omitempty" json:"resource,omitempty"`
	// Description is shown in the dashboard's grant form.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// Skill — a markdown-bodied playbook the agent loads on demand.
// Mirrors Anthropic's open SKILL.md spec (agentskills.io): metadata
// at the top (name, description, optional command), body in
// markdown. Apps can declare the body inline (`body:`) or as a
// repo-relative path (`body_file: skills/foo.md`); the server
// resolves body_file to the file's contents at install time and
// stores the result in the database. After install, the row is the
// source of truth — no runtime filesystem reads.
type Skill struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	// Command — optional slash trigger (e.g. "/storage"). Stored as
	// the literal command including the leading slash. The agent
	// runtime (separate task) routes /<command> invocations to this
	// skill in addition to the description-based match.
	Command string `yaml:"command,omitempty" json:"command,omitempty"`
	// Exactly one of Body / BodyFile must be set on install. After
	// install both fields are unused — the resolved markdown lives
	// in the skills.body column.
	Body     string `yaml:"body,omitempty" json:"body,omitempty"`
	BodyFile string `yaml:"body_file,omitempty" json:"body_file,omitempty"`
	// Free-form metadata: icon, category, tags. Surfaced verbatim by
	// the dashboard for filtering / display.
	Metadata map[string]any `yaml:"metadata,omitempty" json:"metadata,omitempty"`
}

// UIComponent is a small reusable React component the dashboard can
// mount inline (chat tool attachments, sidebar widgets, etc). Apps
// describe what they can render; the agent decides when to invoke
// (via the `respond(components=…)` tool on the chat MCP) for chat
// surfaces, and the dashboard auto-mounts for slot surfaces.
//
// Slots:
//
//	chat.message_attachment   — under an agent message in chat
//	dashboard.project_sidebar — small widget on the project home
//	tool_details.popover      — when an operator clicks a tool row
//
// Slot list is enforced by the platform — components can only render
// in their declared slots. Components without slots can't be rendered
// anywhere; they're effectively dead code (intentional: forces apps
// to be explicit about where their UI shows up).
type UIComponent struct {
	Name        string         `yaml:"name" json:"name"`   // kebab-case, scoped under the app
	Entry       string         `yaml:"entry" json:"entry"` // sidecar path: "/ui/FileCard.mjs"
	Slots       []string       `yaml:"slots" json:"slots"` // allowlist of where it can render
	PropsSchema map[string]any `yaml:"props_schema,omitempty" json:"props_schema,omitempty"`
	// PreviewProps lets the dashboard render a live sample of this
	// component (in the app's install detail panel) so operators can
	// see what the agent will surface in chat without having to
	// trigger a real respond. Optional; when nil the detail panel
	// skips the preview and just lists the metadata.
	//
	// Soft convention for components that need real data to render
	// (e.g. FileCard fetches a file row by id): recognize a
	// `preview: true` prop and render synthetic sample state instead
	// of fetching. Lets a brand-new install with zero data show a
	// useful preview. Components that don't fetch can ignore the
	// convention and put real-looking values in preview_props
	// directly. Components that fetch but ignore the convention
	// will render a skeleton or tombstone — also informative, just
	// less polished.
	PreviewProps map[string]any `yaml:"preview_props,omitempty" json:"preview_props,omitempty"`
}

// RouteSpec — the app sidecar serves these prefixes; platform reverse-
// proxies /apps/<name><prefix> to the sidecar. no_auth lets the
// platform gateway pass anonymous requests through to a route that
// does its own token/signature validation.
type RouteSpec struct {
	Method string `yaml:"method,omitempty" json:"method,omitempty"`
	Prefix string `yaml:"prefix" json:"prefix"`
	NoAuth bool   `yaml:"no_auth,omitempty" json:"no_auth,omitempty"`
}

// MCPToolSpec is the per-tool entry the sidecar's MCP endpoint exposes.
// The platform records one mcp_servers row per app install pointing at
// the sidecar's /mcp; tool listing happens dynamically — the spec here
// is for marketplace display and per-call authorization.
type MCPToolSpec struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	// Requires names a permission from Provides.ProvidedPermissions.
	// When set, the SDK gates this tool on the caller's grants
	// before invoking the handler. Empty = no gate (back-compat).
	Requires string `yaml:"requires,omitempty" json:"requires,omitempty"`
	// ResourceFrom is a template that builds the runtime resource
	// from the call args, e.g. "folder/{arg.folder}". Substituted
	// by the SDK gate; the result is matched against grant.resource
	// using the resource type's Matcher. Empty = handler-side
	// enforcement (gate skipped, app filters returns itself).
	ResourceFrom string `yaml:"resource_from,omitempty" json:"resource_from,omitempty"`
}

// PromptFragment files concatenate into instance directives at boot
// when the app is enabled on an instance.
type PromptFragment struct {
	File     string `yaml:"file" json:"file"`         // path inside app repo
	Position string `yaml:"position" json:"position"` // directive_prefix | directive_suffix | section
}

// UIPanel is mounted into a fixed slot of Apteva's dashboard. The
// host renders the panel — first-party panels are React components
// dynamically resolved by app name, third-party panels fall back to
// an iframe served from the sidecar at Entry. Either way the panel
// inherits the host's auth + project context via props.
//
// Slots:
//
//	project.page    — sidebar entry + full-pane page scoped to a project
//	instance.tab    — full-pane tab inside the agent detail page
//	instance.status — thin status strip on the agent detail header
//	settings.app    — embedded into the Apps tab's per-install detail
type UIPanel struct {
	Slot  string `yaml:"slot" json:"slot"`
	Label string `yaml:"label" json:"label"`
	Icon  string `yaml:"icon" json:"icon"`
	Entry string `yaml:"entry" json:"entry"` // sidecar path used as iframe fallback
}

// UIApp declares a standalone, own-origin UI served at a Traefik
// subdomain — for white-label client portals.
type UIApp struct {
	// DomainTemplate — Traefik-style host pattern for own-origin
	// deployments (e.g. "{tenant}.client.example.com"). Empty value
	// means same-origin, path-mounted under apteva-server itself —
	// see MountPath. The template path is the future white-label flow;
	// the same-origin path is what install_kind=static actually uses.
	DomainTemplate string `yaml:"domain_template" json:"domain_template"`
	// MountPath — used when DomainTemplate is empty. The static bundle
	// is served at apteva-server's `<MountPath>/...` (e.g. "/client").
	// Defaults to "/" + manifest.name when unset; admins can override
	// per-install via config_schema's `mount_path` key.
	MountPath string   `yaml:"mount_path,omitempty" json:"mount_path,omitempty"`
	Auth      string   `yaml:"auth" json:"auth"` // platform | none | own
	Branding  Branding `yaml:"branding" json:"branding"`
}

type Branding struct {
	TitleTemplate string `yaml:"title_template" json:"title_template"`
	Logo          string `yaml:"logo" json:"logo"`
	ThemeCSS      string `yaml:"theme_css" json:"theme_css"`
}

// ChannelSpec — adapter the app contributes (e.g. WhatsApp).
type ChannelSpec struct {
	Name         string   `yaml:"name" json:"name"`
	Capabilities []string `yaml:"capabilities" json:"capabilities"` // text | image | voice
}

// WorkerSpec describes a background goroutine the app runs in its
// sidecar. Schedule mirrors cron-style: "@every 1m", "0 */5 * * *", …
type WorkerSpec struct {
	Name     string `yaml:"name" json:"name"`
	Schedule string `yaml:"schedule" json:"schedule"`
}

// Runtime declares how the sidecar gets started. Three delivery modes
// can be declared together on one manifest; the platform picks the
// best one available:
//
//	source    — primary path for Go apps. Manifest names a git repo +
//	            ref; the platform clones, runs `go build`, caches the
//	            resulting binary under ~/.apteva/apps/<name>/<version>/
//	            and spawns it. Authors push source — no per-platform
//	            builds, no release pipeline. Requires Go on the host.
//
//	binaries  — pre-built native binaries, keyed "<os>-<arch>". The
//	            platform downloads, caches, and spawns. Use when the
//	            app is closed-source or wants a polished release flow.
//
//	image     — fallback for non-Go apps or when extra isolation
//	            matters. Orchestrator deploys the image to a worker.
type Runtime struct {
	// Kind — "service" | "source" | "static". The first two start a
	// sidecar (image pull or git build); "static" means no process at
	// all — the app contributes only assets that apteva-server mounts
	// directly under its own HTTP mux. UI-only apps (single-page
	// portals, marketing kiosks, etc.) pick "static" and skip every
	// field below except StaticDir.
	Kind     string            `yaml:"kind" json:"kind"` // service | source | static
	Image    string            `yaml:"image" json:"image"`
	Binaries map[string]string `yaml:"binaries" json:"binaries"` // key: "<os>-<arch>" e.g. "linux-amd64", "darwin-arm64"
	Source   *SourceSpec       `yaml:"source,omitempty" json:"source,omitempty"`
	// Bundle — prebuilt static-asset tarball delivery for kind: static.
	// CI builds dist/, packs it as <name>-<version>.tgz, uploads to a
	// release; the server downloads, verifies sha256, extracts. Lets
	// authors ship the build artifact instead of the build toolchain,
	// so install hosts don't need bun/node. SHA256 is required — an
	// unverified bundle is a supply-chain hole we're not paying for.
	Bundle *BundleSpec `yaml:"bundle,omitempty" json:"bundle,omitempty"`
	// StaticDir — only meaningful when Kind == "static". Path inside
	// the app repo (relative) or absolute on disk where the prebuilt
	// SPA / asset directory lives. apteva-server serves this as a
	// path-mounted handler with SPA fallback. The directory must
	// exist at install time; for `kind: source` apps we'd build it
	// first, but static apps generally ship a `dist/` already.
	//
	// When Bundle is set, StaticDir is the path *inside the extracted
	// tarball* (commonly "." if the tarball was built with
	// `tar -C dist .`, or "dist" if the tarball preserves the dist/
	// prefix).
	StaticDir   string `yaml:"static_dir,omitempty" json:"static_dir,omitempty"`
	Port        int    `yaml:"port" json:"port"`
	HealthCheck string `yaml:"health_check" json:"health_check"`
	// BindHost — interface the sidecar listens on. Default loopback
	// ("127.0.0.1") because predictable APTEVA_APP_TOKENs (dev-<id>
	// form) make wider exposure risky for most apps; the platform
	// proxies LAN/WAN traffic to /api/apps/<name>/* through its own
	// auth layer, so loopback is the safe choice. Apps that genuinely
	// need LAN reachability (DLNA broadcaster, ESSDP responder, any
	// "talk to TVs / IoT devices on the same subnet" use case) set
	// "0.0.0.0" or a specific interface IP. Mark every NoAuth route
	// explicitly when doing this — the host's network IS the auth
	// boundary in that mode.
	BindHost  string             `yaml:"bind_host,omitempty" json:"bind_host,omitempty"`
	Resources ResourceLimits     `yaml:"resources" json:"resources"`
	Storage   []StorageSpec      `yaml:"storage" json:"storage"`
	Env       map[string]EnvFrom `yaml:"env" json:"env"`
}

// SourceSpec — git-clone-and-build delivery. Paired with kind: source.
// Repo is the canonical module path ("github.com/apteva/app-tasks");
// the platform turns it into a clone URL. Ref is a tag, branch, or
// commit SHA; for branches the supervisor re-clones on each install
// so 'main' tracks tip-of-tree. Entry is the `go build` target inside
// the repo (default ".").
type SourceSpec struct {
	Repo  string `yaml:"repo" json:"repo"`
	Ref   string `yaml:"ref" json:"ref"`
	Entry string `yaml:"entry" json:"entry"`
}

// BundleSpec — prebuilt static-asset tarball delivery. URL points at a
// gzipped tar archive (.tgz / .tar.gz). SHA256 is a hex digest of the
// raw archive bytes; the server refuses to extract on mismatch. URLs
// are expected to be immutable (release-asset pattern); the cache
// keys on <name>/<version> and skips re-download when the marker file
// matches the manifest's sha256.
type BundleSpec struct {
	URL    string `yaml:"url" json:"url"`
	SHA256 string `yaml:"sha256" json:"sha256"`
}

type ResourceLimits struct {
	CPU         float64 `yaml:"cpu" json:"cpu"`
	Memory      int     `yaml:"memory" json:"memory"`
	CPULimit    float64 `yaml:"cpu_limit" json:"cpu_limit"`
	MemoryLimit int     `yaml:"memory_limit" json:"memory_limit"`
}

type StorageSpec struct {
	Name      string `yaml:"name" json:"name"`
	MountPath string `yaml:"mount_path" json:"mount_path"`
}

// EnvFrom — a manifest env entry. Either a literal value or a platform-
// injected token (gateway URL, app token, install id).
type EnvFrom struct {
	Value string `yaml:"value,omitempty" json:"value,omitempty"`
	From  string `yaml:"from,omitempty" json:"from,omitempty"` // "platform" → server fills in
}

// DBConfig describes the app's private database. Default driver is
// sqlite; declare postgres to ask the orchestrator for a Postgres
// schema. The platform runs migrations/* on first install.
//
// On-disk layout invariant (read this before adding any path that
// looks like the version cleanup logic in
// apps/source-installer):
//
//	apps/<name>/<version>/        — built source tree + binary; one
//	                                per installed version, the
//	                                prune sweep ages these out.
//	apps/<name>/data/<install>/   — RESERVED for per-install
//	                                persistent state (app.db,
//	                                APTEVA_DATA_DIR contents). The
//	                                platform sets DB_PATH and
//	                                APTEVA_DATA_DIR to point here.
//	                                MUST NOT be touched by any
//	                                version-dir cleanup — destroying
//	                                it silently wipes every install's
//	                                SQLite DB (we shipped the bug,
//	                                we shipped the fix in v0.14.3).
//
// If you add a new sibling under apps/<name>/, register it in
// reservedAppSiblings in server/apps_source.go AND make sure its
// name doesn't accidentally match the `^\d+\.\d+\.\d+(...)?$`
// version pattern. Belt-and-suspenders: pattern guard rejects
// non-semver names; allowlist catches anything that ever gets
// through.
type DBConfig struct {
	Driver     string `yaml:"driver" json:"driver"` // sqlite | postgres
	Path       string `yaml:"path" json:"path"`
	Migrations string `yaml:"migrations" json:"migrations"`
}

// ConfigField — a single field in the install-time configuration form
// the dashboard renders against the user. Same schema is reused for
// after-install settings edits via PUT /api/apps/installs/:id/config.
type ConfigField struct {
	Name  string `yaml:"name" json:"name"`
	Label string `yaml:"label" json:"label"`
	// Type names recognised by the dashboard renderer:
	//   text | password | toggle | select | gdrive_sheet |
	//   gdrive_folder | select_from_integration | select_from_app
	// Unknown types fall back to a plain text input. New types
	// must be added in two places: this comment + the dashboard's
	// SettingsSection / InstallModal switch arms.
	Type        string `yaml:"type" json:"type"`
	Description string `yaml:"description" json:"description"`
	Required    bool   `yaml:"required" json:"required"`
	Default     string `yaml:"default" json:"default"`
	// Options — closed enum for `type: select`. Each entry is a single
	// string; the form renders a dropdown and the dashboard validates
	// that the chosen value is one of these. Ignored for other types.
	Options []string `yaml:"options,omitempty" json:"options,omitempty"`

	// RequiredIfRoleBound — name of an integration role from
	// requires.integrations[].role. The field becomes required only
	// when that role has a non-null binding. Lets a manifest mark
	// `s3_bucket` required only when the optional `backend` role is
	// bound, without forcing operators to fill in a bucket for the
	// disk-backed install. Empty = unconditional (use Required
	// instead).
	RequiredIfRoleBound string `yaml:"required_if_role_bound,omitempty" json:"required_if_role_bound,omitempty"`

	// IntegrationRole — for type=select_from_integration: which
	// requires.integrations[].role to draw the discovery list from.
	// The dashboard reads the connection bound to that role and
	// invokes Discovery.Tool against it to populate the dropdown.
	IntegrationRole string `yaml:"integration_role,omitempty" json:"integration_role,omitempty"`

	// App — for type=select_from_app: which sibling app (by name)
	// to query for the dropdown options. The dashboard hits
	// /api/apps/<App><Discovery.Route> over its existing same-origin
	// session and parses the response via Discovery.ResponsePath +
	// ValueField + LabelField (same shape as the integration variant
	// so manifest authors only learn one mental model). The named
	// app should appear in requires.apps so the install picker can
	// surface a "missing dep" error rather than a silent dropdown
	// fail at config time.
	App string `yaml:"app,omitempty" json:"app,omitempty"`

	// Discovery — for type=select_from_integration AND
	// type=select_from_app: how to fetch the list of options. The
	// integration variant uses Discovery.Tool against the bound
	// connection's API; the app variant uses Discovery.Route against
	// the sibling app's HTTP surface. ResponsePath JSON-paths to the
	// list inside the response (or "" for a flat array); ValueField +
	// LabelField pick each item's dropdown value/label. One Discovery
	// struct serves both variants — readers pick Tool vs Route based
	// on the field's Type.
	Discovery *ConfigFieldDiscovery `yaml:"discovery,omitempty" json:"discovery,omitempty"`

	// Fallback — what to render when discovery fails (no binding,
	// upstream returned an error, the response was empty, etc.).
	// "text" → render a plain text input so the operator can type
	// the value manually. Empty → just show the error message and
	// disable the field.
	Fallback string `yaml:"fallback,omitempty" json:"fallback,omitempty"`
}

// ConfigFieldDiscovery — the "fetch a list at install time"
// declaration for type=select_from_integration. Same shape as
// catalog-level credential_group.discovery but applied per
// install field rather than per app suite.
type ConfigFieldDiscovery struct {
	// Tool to invoke against the bound connection (only for
	// type=select_from_integration). Must exist in the bound app's
	// tools[] AND require zero arguments (the dashboard sends an
	// empty input map). Typically a list_X / list_buckets /
	// list_workspaces style endpoint.
	Tool string `yaml:"tool,omitempty" json:"tool,omitempty"`
	// Route on the sibling app (only for type=select_from_app).
	// The dashboard hits /api/apps/<ConfigField.App><Route> over
	// its same-origin session. Typically a GET on a list endpoint
	// the app already exposes for its own panel. Must include the
	// leading slash, e.g. "/api/instances".
	Route string `yaml:"route,omitempty" json:"route,omitempty"`
	// JSON path into the response body to get the array of items.
	// Dot-separated; "[]" steps into every element of an array.
	// Empty string = the response itself is the array. Examples:
	//   "ListAllMyBucketsResult.Buckets.Bucket"   (S3 XML)
	//   "data"                                    (typical REST)
	//   ""                                        (raw array)
	ResponsePath string `yaml:"response_path,omitempty" json:"response_path,omitempty"`
	// Field on each item to use as the dropdown's value (saved into
	// config). Empty = use the item itself if it's a string,
	// otherwise its first string field.
	ValueField string `yaml:"value_field,omitempty" json:"value_field,omitempty"`
	// Field on each item to use as the dropdown's label. Empty =
	// same as value.
	LabelField string `yaml:"label_field,omitempty" json:"label_field,omitempty"`
}

// UpgradePolicy is the per-install default; users can override in the
// dashboard. Permission deltas force re-consent regardless.
type UpgradePolicy string

const (
	UpgradeManual    UpgradePolicy = "manual"
	UpgradeAutoPatch UpgradePolicy = "auto-patch"
	UpgradeAutoMinor UpgradePolicy = "auto-minor"
)

// Permission is one entry in the fixed taxonomy enforced by the
// platform's PlatformAPI. Apps may only call the slice they declared.
type Permission string

const (
	PermDBWriteApp         Permission = "db.write.app"
	PermNetEgress          Permission = "net.egress"
	PermConnectionsRead    Permission = "platform.connections.read"
	PermConnectionsWrite   Permission = "platform.connections.write"
	PermConnectionsExecute Permission = "platform.connections.execute"
	PermInstancesRead      Permission = "platform.instances.read"
	PermInstancesWrite     Permission = "platform.instances.write"
	PermMCPAttach          Permission = "platform.mcp.attach"
	PermChannelsSend       Permission = "platform.channels.send"
	PermAppsCall           Permission = "platform.apps.call"
	PermFSReadShared       Permission = "fs.read.shared"
	PermFSWriteShared      Permission = "fs.write.shared"
	// PermOAuthStart lets an app initiate an OAuth dance against any
	// integration in the catalog and store the resulting connection
	// under its own ownership (created_via=app_install). Bundled with
	// platform.connections.manage so the app can list / disconnect /
	// refresh the connections it owns.
	PermOAuthStart        Permission = "platform.oauth.start"
	PermConnectionsManage Permission = "platform.connections.manage"
	// PermConnectionsReadCredentials lets an app read raw decrypted
	// credentials from a bound integration connection. Reserved for
	// apps whose access pattern can't go through the integration
	// runner (multipart uploads, presigned URLs, range GETs).
	PermConnectionsReadCredentials Permission = "platform.connections.read_credentials"
	// PermRealtimeSpawn lets an app spawn realtime (voice/audio) sub-
	// threads via PlatformClient.SpawnRealtimeThread, and kill any
	// thread it created via KillThread. Implicit cost surface — the
	// realtime model is billed per audio token — so the operator
	// must opt in by installing the app AND flipping the instance's
	// Config.RealtimeEnabled flag.
	PermRealtimeSpawn Permission = "platform.realtime.spawn"
	// PermEnvironmentsRead lets an app inspect live test/backtest
	// environments visible to its installing user.
	PermEnvironmentsRead Permission = "platform.environments.read"
	// PermEnvironmentsCall lets an app seed or call tools inside an
	// environment. This is the narrow execution surface for backtest
	// runners and evaluators.
	PermEnvironmentsCall Permission = "platform.environments.call"
	// PermEnvironmentsManage lets an app create, snapshot, spawn/stop
	// environment agents, and destroy environments. Treat this as a
	// control-plane permission; most apps should not need it.
	PermEnvironmentsManage Permission = "platform.environments.manage"
	// PermIngressRead lets an app inspect public hostnames it owns.
	PermIngressRead Permission = "platform.ingress.read"
	// PermIngressWrite lets an app expose and unexpose hostnames
	// through server-native ingress and managed certificate issuance.
	PermIngressWrite Permission = "platform.ingress.write"
	// PermDNSRead lets an app inspect delegated DNS zones available
	// through the platform. This is intentionally capability-level:
	// the backing authority may be a local Domains app, Fleet, or a
	// parent hosting controller.
	PermDNSRead Permission = "platform.dns.read"
	// PermDNSWrite lets an app upsert/delete DNS records through a
	// scoped platform grant. The server validates the requested record
	// against the grant before forwarding to the backing controller.
	PermDNSWrite Permission = "platform.dns.write"
)

// AllPermissions returns the full taxonomy — used by the dashboard's
// install consent screen to render plain-English descriptions.
func AllPermissions() []Permission {
	return []Permission{
		PermDBWriteApp, PermNetEgress,
		PermConnectionsRead, PermConnectionsWrite, PermConnectionsExecute,
		PermInstancesRead, PermInstancesWrite,
		PermMCPAttach, PermChannelsSend, PermAppsCall,
		PermFSReadShared, PermFSWriteShared,
		PermOAuthStart, PermConnectionsManage,
		PermConnectionsReadCredentials,
		PermRealtimeSpawn,
		PermEnvironmentsRead, PermEnvironmentsCall, PermEnvironmentsManage,
		PermIngressRead, PermIngressWrite,
		PermDNSRead, PermDNSWrite,
	}
}

// LoadManifest reads + validates an apteva.yaml at the given path.
func LoadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	return ParseManifest(b)
}

// ParseManifest decodes + validates a manifest from raw bytes. Used by
// both apps (validating their own manifest at boot) and the server
// (validating incoming installs from git URLs).
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true) // unknown field = hard error, fail closed
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parse manifest yaml: %w", err)
	}
	if err := ValidateManifest(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// ValidateManifest enforces the static rules a manifest must satisfy
// independent of the deployment context. Dynamic checks (image exists,
// permission scope agrees with what the user consented) live elsewhere.
func ValidateManifest(m *Manifest) error {
	if m.Schema != SchemaCurrent {
		return fmt.Errorf("schema %q not supported (this SDK speaks %s)", m.Schema, SchemaCurrent)
	}
	if !isSlug(m.Name) {
		return errors.New("name must be a lowercase slug (a-z0-9-)")
	}
	if m.DisplayName == "" {
		m.DisplayName = m.Name
	}
	if m.Version == "" {
		return errors.New("version required")
	}
	if len(m.Scopes) == 0 {
		m.Scopes = []Scope{ScopeProject}
	}
	for _, s := range m.Scopes {
		if s != ScopeProject && s != ScopeGlobal {
			return fmt.Errorf("scope %q must be project or global", s)
		}
	}
	for _, p := range m.Requires.Permissions {
		if !knownPermission(p) {
			return fmt.Errorf("unknown permission %q", p)
		}
	}
	if m.Runtime.Kind != "" && m.Runtime.Kind != "service" && m.Runtime.Kind != "source" && m.Runtime.Kind != "static" {
		return fmt.Errorf("runtime.kind %q unsupported (service | source | static)", m.Runtime.Kind)
	}
	if m.Runtime.Kind == "static" {
		if m.Runtime.StaticDir == "" {
			return errors.New("runtime.static_dir required when kind=static")
		}
		// Need *somewhere* the bundle can be found at install time.
		// Absolute static_dir = baked-in; bundle = prebuilt tarball;
		// source = clone-on-install (dev / authoring loop, no build).
		if !filepath.IsAbs(m.Runtime.StaticDir) &&
			m.Runtime.Bundle == nil &&
			m.Runtime.Source == nil {
			return errors.New("runtime.static_dir is relative — set runtime.bundle (prebuilt tarball) or runtime.source (clone-on-install) so the server can find it")
		}
	}
	if m.Runtime.Bundle != nil {
		if m.Runtime.Kind != "static" {
			return errors.New("runtime.bundle only supported for kind=static")
		}
		if m.Runtime.Bundle.URL == "" {
			return errors.New("runtime.bundle.url required")
		}
		if m.Runtime.Bundle.SHA256 == "" {
			return errors.New("runtime.bundle.sha256 required — unverified bundles are not allowed")
		}
		if len(m.Runtime.Bundle.SHA256) != 64 {
			return fmt.Errorf("runtime.bundle.sha256 must be 64 hex chars (got %d)", len(m.Runtime.Bundle.SHA256))
		}
	}
	if m.Runtime.Kind == "source" {
		if m.Runtime.Source == nil || m.Runtime.Source.Repo == "" {
			return errors.New("runtime.source.repo required when kind=source")
		}
		if m.Runtime.Port == 0 {
			return errors.New("runtime.port required when kind=source")
		}
	}
	if m.Runtime.Kind == "service" {
		// At least one delivery mode must be declared.
		if m.Runtime.Image == "" && len(m.Runtime.Binaries) == 0 && m.Runtime.Source == nil {
			return errors.New("runtime requires source, binaries, or image")
		}
		if m.Runtime.Port == 0 {
			return errors.New("runtime.port required when kind=service")
		}
	}
	if m.UpgradePolicy == "" {
		m.UpgradePolicy = UpgradeManual
	}
	switch m.UpgradePolicy {
	case UpgradeManual, UpgradeAutoPatch, UpgradeAutoMinor:
	default:
		return fmt.Errorf("upgrade_policy %q invalid", m.UpgradePolicy)
	}
	if err := validateProvidedPermissions(&m.Provides); err != nil {
		return err
	}
	return nil
}

// validateProvidedPermissions checks that the permission/resource graph
// is internally consistent: every ProvidedPermission.Resource refers to
// a declared ResourceDecl, every MCPToolSpec.Requires names a declared
// permission, matchers + pickers come from the known vocabulary.
//
// Apps that don't opt in (no resources, no permissions) skip every
// check below.
func validateProvidedPermissions(p *Provides) error {
	resourceTypes := make(map[string]*ResourceDecl, len(p.Resources))
	for i := range p.Resources {
		r := &p.Resources[i]
		if r.Name == "" {
			return errors.New("provides.resources[].name required")
		}
		if _, dup := resourceTypes[r.Name]; dup {
			return fmt.Errorf("provides.resources: duplicate name %q", r.Name)
		}
		switch r.Matcher {
		case "glob", "id_set", "prefix", "tag_set", "exact":
		case "":
			return fmt.Errorf("provides.resources[%q].matcher required (glob|id_set|prefix|tag_set|exact)", r.Name)
		default:
			return fmt.Errorf("provides.resources[%q].matcher %q unsupported", r.Name, r.Matcher)
		}
		switch r.Picker {
		case "", "tree", "list", "search", "tags", "freeform":
		default:
			return fmt.Errorf("provides.resources[%q].picker %q unsupported", r.Name, r.Picker)
		}
		switch r.ListingVisibility {
		case "", "navigable", "scoped_only", "none":
		default:
			return fmt.Errorf("provides.resources[%q].listing_visibility %q unsupported", r.Name, r.ListingVisibility)
		}
		resourceTypes[r.Name] = r
	}
	permissions := make(map[string]*ProvidedPermission, len(p.ProvidedPermissions))
	for i := range p.ProvidedPermissions {
		perm := &p.ProvidedPermissions[i]
		if perm.Name == "" {
			return errors.New("provides.permissions[].name required")
		}
		if _, dup := permissions[perm.Name]; dup {
			return fmt.Errorf("provides.permissions: duplicate name %q", perm.Name)
		}
		if perm.Resource != "" {
			if _, ok := resourceTypes[perm.Resource]; !ok {
				return fmt.Errorf("provides.permissions[%q].resource %q not declared in provides.resources", perm.Name, perm.Resource)
			}
		}
		permissions[perm.Name] = perm
	}
	for i := range p.MCPTools {
		t := &p.MCPTools[i]
		if t.Requires == "" {
			continue
		}
		if _, ok := permissions[t.Requires]; !ok {
			return fmt.Errorf("provides.mcp_tools[%q].requires %q not declared in provides.permissions", t.Name, t.Requires)
		}
		// ResourceFrom is informational at parse time — substitution
		// happens at call time. Anything goes here.
	}
	return nil
}

func isSlug(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	return true
}

func knownPermission(p Permission) bool {
	for _, k := range AllPermissions() {
		if k == p {
			return true
		}
	}
	return false
}
