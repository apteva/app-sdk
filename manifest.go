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
	Schema      string `yaml:"schema" json:"schema"`
	Name        string `yaml:"name" json:"name"`               // slug; matches repo and orchestrator service
	DisplayName string `yaml:"display_name" json:"display_name"`
	Version     string `yaml:"version" json:"version"`         // semver
	Description string `yaml:"description" json:"description"`
	Author      string `yaml:"author" json:"author"`
	Homepage    string `yaml:"homepage" json:"homepage"`
	Icon        string `yaml:"icon" json:"icon"`
	Tags        []string `yaml:"tags" json:"tags"`

	Scopes           []Scope `yaml:"scopes" json:"scopes"`
	MinAptevaVersion string  `yaml:"min_apteva_version" json:"min_apteva_version"`

	Requires Requires `yaml:"requires" json:"requires"`
	Provides Provides `yaml:"provides" json:"provides"`

	Runtime      Runtime      `yaml:"runtime" json:"runtime"`
	DB           *DBConfig    `yaml:"db,omitempty" json:"db,omitempty"`
	ConfigSchema []ConfigField `yaml:"config_schema" json:"config_schema"`

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
	Permissions       []Permission        `yaml:"permissions" json:"permissions"`
	MCPToolsAtRuntime []string            `yaml:"mcp_tools_at_runtime" json:"mcp_tools_at_runtime"`
	Apps              []RequiredAppRef    `yaml:"apps,omitempty" json:"apps,omitempty"`
	// Integrations declares roles this app fills with either an
	// integration connection or another Apteva app. The operator
	// binds each role at install time; the app reads the binding
	// at runtime via ctx.IntegrationFor(role) and never sees raw
	// credentials. See IntegrationDep below.
	Integrations []IntegrationDep `yaml:"integrations,omitempty" json:"integrations,omitempty"`
}

// IntegrationDep declares one role the app needs filled. Two kinds:
//
//   kind: integration  — bind a connection (per-project credentials
//                        for some upstream like OpenAI). The platform
//                        executes tools server-side via the existing
//                        integration runner; the app never holds the
//                        secret.
//
//   kind: app          — bind another Apteva app installed in the
//                        same project. The platform proxies MCP calls
//                        from this app to the target sidecar; auth is
//                        the binding itself.
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
	Name     string `yaml:"name" json:"name"`                         // matches the dep's manifest.name
	Version  string `yaml:"version,omitempty" json:"version,omitempty"` // semver constraint, ">=1.0.0" form; empty = any
	Reason   string `yaml:"reason,omitempty" json:"reason,omitempty"`   // human-readable; surfaced in the dashboard
	Optional bool   `yaml:"optional,omitempty" json:"optional,omitempty"`
}

// Provides describes the surfaces this app contributes back to the
// platform — none, one, or many.
type Provides struct {
	HTTPRoutes      []RouteSpec       `yaml:"http_routes" json:"http_routes"`
	MCPTools        []MCPToolSpec     `yaml:"mcp_tools" json:"mcp_tools"`
	PromptFragments []PromptFragment  `yaml:"prompt_fragments" json:"prompt_fragments"`
	UIPanels        []UIPanel         `yaml:"ui_panels" json:"ui_panels"`
	UIComponents    []UIComponent     `yaml:"ui_components,omitempty" json:"ui_components,omitempty"`
	UIApp           *UIApp            `yaml:"ui_app,omitempty" json:"ui_app,omitempty"`
	Channels        []ChannelSpec     `yaml:"channels" json:"channels"`
	Workers         []WorkerSpec      `yaml:"workers" json:"workers"`
	// Skills the app ships — markdown-bodied playbooks the agent
	// loads on demand to act with this app's expertise. Each entry
	// becomes one row in the platform's skills table on install,
	// refreshes on upgrade, cascade-deletes on uninstall.
	Skills          []Skill           `yaml:"skills,omitempty" json:"skills,omitempty"`
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
	Name        string         `yaml:"name" json:"name"`
	Description string         `yaml:"description" json:"description"`
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
//   chat.message_attachment   — under an agent message in chat
//   dashboard.project_sidebar — small widget on the project home
//   tool_details.popover      — when an operator clicks a tool row
//
// Slot list is enforced by the platform — components can only render
// in their declared slots. Components without slots can't be rendered
// anywhere; they're effectively dead code (intentional: forces apps
// to be explicit about where their UI shows up).
type UIComponent struct {
	Name        string         `yaml:"name" json:"name"`               // kebab-case, scoped under the app
	Entry       string         `yaml:"entry" json:"entry"`             // sidecar path: "/ui/FileCard.mjs"
	Slots       []string       `yaml:"slots" json:"slots"`             // allowlist of where it can render
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
// proxies /apps/<name><prefix> to the sidecar.
type RouteSpec struct {
	Prefix string `yaml:"prefix" json:"prefix"`
}

// MCPToolSpec is the per-tool entry the sidecar's MCP endpoint exposes.
// The platform records one mcp_servers row per app install pointing at
// the sidecar's /mcp; tool listing happens dynamically — the spec here
// is for marketplace display only.
type MCPToolSpec struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
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
//   project.page    — sidebar entry + full-pane page scoped to a project
//   instance.tab    — full-pane tab inside the agent detail page
//   instance.status — thin status strip on the agent detail header
//   settings.app    — embedded into the Apps tab's per-install detail
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
	DomainTemplate string   `yaml:"domain_template" json:"domain_template"`
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
//   source    — primary path for Go apps. Manifest names a git repo +
//               ref; the platform clones, runs `go build`, caches the
//               resulting binary under ~/.apteva/apps/<name>/<version>/
//               and spawns it. Authors push source — no per-platform
//               builds, no release pipeline. Requires Go on the host.
//
//   binaries  — pre-built native binaries, keyed "<os>-<arch>". The
//               platform downloads, caches, and spawns. Use when the
//               app is closed-source or wants a polished release flow.
//
//   image     — fallback for non-Go apps or when extra isolation
//               matters. Orchestrator deploys the image to a worker.
type Runtime struct {
	// Kind — "service" | "source" | "static". The first two start a
	// sidecar (image pull or git build); "static" means no process at
	// all — the app contributes only assets that apteva-server mounts
	// directly under its own HTTP mux. UI-only apps (single-page
	// portals, marketing kiosks, etc.) pick "static" and skip every
	// field below except StaticDir.
	Kind        string             `yaml:"kind" json:"kind"`             // service | source | static
	Image       string             `yaml:"image" json:"image"`
	Binaries    map[string]string  `yaml:"binaries" json:"binaries"`     // key: "<os>-<arch>" e.g. "linux-amd64", "darwin-arm64"
	Source      *SourceSpec        `yaml:"source,omitempty" json:"source,omitempty"`
	// Bundle — prebuilt static-asset tarball delivery for kind: static.
	// CI builds dist/, packs it as <name>-<version>.tgz, uploads to a
	// release; the server downloads, verifies sha256, extracts. Lets
	// authors ship the build artifact instead of the build toolchain,
	// so install hosts don't need bun/node. SHA256 is required — an
	// unverified bundle is a supply-chain hole we're not paying for.
	Bundle      *BundleSpec        `yaml:"bundle,omitempty" json:"bundle,omitempty"`
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
	StaticDir   string             `yaml:"static_dir,omitempty" json:"static_dir,omitempty"`
	Port        int                `yaml:"port" json:"port"`
	HealthCheck string             `yaml:"health_check" json:"health_check"`
	Resources   ResourceLimits     `yaml:"resources" json:"resources"`
	Storage     []StorageSpec      `yaml:"storage" json:"storage"`
	Env         map[string]EnvFrom `yaml:"env" json:"env"`
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
type DBConfig struct {
	Driver     string `yaml:"driver" json:"driver"` // sqlite | postgres
	Path       string `yaml:"path" json:"path"`
	Migrations string `yaml:"migrations" json:"migrations"`
}

// ConfigField — a single field in the install-time configuration form
// the dashboard renders against the user. Same schema is reused for
// after-install settings edits via PUT /api/apps/installs/:id/config.
type ConfigField struct {
	Name        string `yaml:"name" json:"name"`
	Label       string `yaml:"label" json:"label"`
	Type        string `yaml:"type" json:"type"` // text | password | toggle | select | gdrive_sheet | gdrive_folder | …
	Description string `yaml:"description" json:"description"`
	Required    bool   `yaml:"required" json:"required"`
	Default     string `yaml:"default" json:"default"`
	// Options — closed enum for `type: select`. Each entry is a single
	// string; the form renders a dropdown and the dashboard validates
	// that the chosen value is one of these. Ignored for other types.
	Options []string `yaml:"options,omitempty" json:"options,omitempty"`
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
	PermDBWriteApp           Permission = "db.write.app"
	PermNetEgress            Permission = "net.egress"
	PermConnectionsRead      Permission = "platform.connections.read"
	PermConnectionsWrite     Permission = "platform.connections.write"
	PermConnectionsExecute   Permission = "platform.connections.execute"
	PermInstancesRead        Permission = "platform.instances.read"
	PermInstancesWrite       Permission = "platform.instances.write"
	PermMCPAttach            Permission = "platform.mcp.attach"
	PermChannelsSend         Permission = "platform.channels.send"
	PermAppsCall             Permission = "platform.apps.call"
	PermFSReadShared         Permission = "fs.read.shared"
	PermFSWriteShared        Permission = "fs.write.shared"
	// PermOAuthStart lets an app initiate an OAuth dance against any
	// integration in the catalog and store the resulting connection
	// under its own ownership (created_via=app_install). Bundled with
	// platform.connections.manage so the app can list / disconnect /
	// refresh the connections it owns.
	PermOAuthStart           Permission = "platform.oauth.start"
	PermConnectionsManage    Permission = "platform.connections.manage"
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
