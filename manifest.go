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
// MCP tools the user must attach, minimum platform version.
type Requires struct {
	Permissions       []Permission `yaml:"permissions" json:"permissions"`
	MCPToolsAtRuntime []string     `yaml:"mcp_tools_at_runtime" json:"mcp_tools_at_runtime"`
}

// Provides describes the surfaces this app contributes back to the
// platform — none, one, or many.
type Provides struct {
	HTTPRoutes      []RouteSpec       `yaml:"http_routes" json:"http_routes"`
	MCPTools        []MCPToolSpec     `yaml:"mcp_tools" json:"mcp_tools"`
	PromptFragments []PromptFragment  `yaml:"prompt_fragments" json:"prompt_fragments"`
	UIPanels        []UIPanel         `yaml:"ui_panels" json:"ui_panels"`
	UIPages         []UIPage          `yaml:"ui_pages" json:"ui_pages"`
	UIApp           *UIApp            `yaml:"ui_app,omitempty" json:"ui_app,omitempty"`
	Channels        []ChannelSpec     `yaml:"channels" json:"channels"`
	Workers         []WorkerSpec      `yaml:"workers" json:"workers"`
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

// UIPanel is a UMD bundle mounted into a fixed slot in Apteva's
// dashboard (first-party trust required).
type UIPanel struct {
	Slot  string `yaml:"slot" json:"slot"`   // settings.app | instance.tab | sidebar.widget
	Label string `yaml:"label" json:"label"`
	Icon  string `yaml:"icon" json:"icon"`
	Entry string `yaml:"entry" json:"entry"` // path served by sidecar (e.g. /ui/AppPanel.umd.js)
}

// UIPage adds a top-level nav entry; iframe-mounted under Apteva's
// frame so third-party UIs are sandboxed.
type UIPage struct {
	Path  string `yaml:"path" json:"path"`
	Label string `yaml:"label" json:"label"`
	Icon  string `yaml:"icon" json:"icon"`
	Entry string `yaml:"entry" json:"entry"` // path the iframe loads
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
	// StaticDir — only meaningful when Kind == "static". Path inside
	// the app repo (relative) or absolute on disk where the prebuilt
	// SPA / asset directory lives. apteva-server serves this as a
	// path-mounted handler with SPA fallback. The directory must
	// exist at install time; for `kind: source` apps we'd build it
	// first, but static apps generally ship a `dist/` already.
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
// the dashboard renders against the user.
type ConfigField struct {
	Name        string `yaml:"name" json:"name"`
	Label       string `yaml:"label" json:"label"`
	Type        string `yaml:"type" json:"type"` // text | password | gdrive_sheet | gdrive_folder | …
	Description string `yaml:"description" json:"description"`
	Required    bool   `yaml:"required" json:"required"`
	Default     string `yaml:"default" json:"default"`
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
	PermInstancesRead        Permission = "platform.instances.read"
	PermInstancesWrite       Permission = "platform.instances.write"
	PermMCPAttach            Permission = "platform.mcp.attach"
	PermChannelsSend         Permission = "platform.channels.send"
	PermFSReadShared         Permission = "fs.read.shared"
	PermFSWriteShared        Permission = "fs.write.shared"
)

// AllPermissions returns the full taxonomy — used by the dashboard's
// install consent screen to render plain-English descriptions.
func AllPermissions() []Permission {
	return []Permission{
		PermDBWriteApp, PermNetEgress,
		PermConnectionsRead, PermConnectionsWrite,
		PermInstancesRead, PermInstancesWrite,
		PermMCPAttach, PermChannelsSend,
		PermFSReadShared, PermFSWriteShared,
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
