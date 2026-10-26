# Apteva App SDK

The public Go SDK for building Apteva Apps — installable units that
extend an Apteva platform with HTTP routes, MCP tools, channels,
prompt fragments, UI panels, and workers.

Apps depend on this module only — never on `apteva-server` internals.

For optional independently deployed services owned by an app, see
[owned app components](docs/components.md).

For verified client addresses in HTTP and WebSocket handlers, see
[client IP metadata](docs/client-ip.md).

Apteva apps can also be packaged as additive Agent Plugins 1.0.0 packages.
See [AGENT_PLUGINS.md](AGENT_PLUGINS.md); existing `apteva.yaml` packages and
install URLs remain fully supported.

## Minimal app

```go
package main

import (
    sdk "github.com/apteva/app-sdk"
    _ "modernc.org/sqlite"   // blank-import a sqlite driver if you use db
)

type MyApp struct{}

func (a *MyApp) Manifest() sdk.Manifest        { return loadManifestFromYAML() }
func (a *MyApp) OnMount(*sdk.AppCtx) error     { return nil }
func (a *MyApp) OnUnmount(*sdk.AppCtx) error   { return nil }
func (a *MyApp) HTTPRoutes() []sdk.Route       { return nil }
func (a *MyApp) MCPTools() []sdk.Tool {
    return []sdk.Tool{{
        Name: "hello",
        Description: "Say hi",
        InputSchema: map[string]any{"type": "object"},
        Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
            return "hi from " + ctx.Manifest().Name, nil
        },
    }}
}
func (a *MyApp) Channels() []sdk.ChannelFactory  { return nil }
func (a *MyApp) Workers() []sdk.Worker           { return nil }
func (a *MyApp) EventHandlers() []sdk.EventHandler { return nil }

func main() { sdk.Run(&MyApp{}) }
```

## Native MCP tool content

Tools return ordinary Go values as JSON inside an MCP text block by default.
Return `sdk.MCPResult` (or its pointer) to supply text and actual image content
to a vision-capable calling model:

```go
return sdk.MCPResult{
    Content: []sdk.MCPContent{
        {Type: "text", Text: "Page 1 of 3"},
        {Type: "image", MIMEType: "image/jpeg", Data: imageBase64},
    },
    StructuredContent: map[string]any{"total_pages": 3},
}, nil
```

`Data` contains standard base64 without a data-URL prefix. The SDK validates
image encoding and preserves empty text. Ordinary maps, including maps with a
`content` field, keep their existing JSON-as-text response. Tool permissions,
caller identity, project context, and RPC errors use the same execution path.

Negotiated app-to-app JSON and internal batches retain the complete result.
For older MCP-wrapped callbacks, pass `*sdk.MCPResult` to `CallAppResult` to
preserve every block. Its `IsError` flag remains available to the consumer.

## What the platform injects at boot

The orchestrator-deployed sidecar receives:

| Env var | Purpose |
|---|---|
| `APTEVA_GATEWAY_URL` | Where to call back to apteva-server |
| `APTEVA_APP_TOKEN` | Bearer token for both inbound (from platform) and outbound (PlatformAPI calls). Short-lived. |
| `APTEVA_INSTALL_ID` | Numeric id of this install row |
| `APTEVA_PROJECT_ID` | The install's project, or empty for global installs |
| `APTEVA_APP_CONFIG` | JSON-encoded user-supplied config (see `config_schema`) |

Plus any literal `env:` entries from the manifest.

## Surfaces an app can declare

Set the relevant block in `apteva.yaml` (see `manifest.go` for the full schema):

- `mcp_tools` — agents call them
- `http_routes` — proxied at `/apps/<name>/<prefix>`
- `prompt_fragments` — concatenated into instance directives
- `ui_panels` — UMD bundle into Apteva's dashboard slot
- `ui_surfaces` — code-free, declarative native UI for iOS and Android
- `exports` — versioned read-only summaries/entities for discoverable app contributions
- `ui_pages` — iframe-mounted top-level nav entry
- `ui_app` — own subdomain via Traefik (white-label)
- `channels` — inbound + outbound message adapters
- `workers` — background goroutines, cron-style schedule

Anything you don't declare — leave the field out or return `nil` from
the matching `App` method.

### MCP tool annotations

Set `Tool.Annotations` to standard MCP hints, for example
`map[string]any{"readOnlyHint": true, "destructiveHint": false}` for a
query that does not change state. The SDK emits these under `annotations` in
`tools/list`, separate from the `_meta` extension fields. Unannotated tools keep
their existing response shape. These hints describe behavior, not execution
permissions; only annotate a tool as read-only if its handler has no writes.

See [CONTRIBUTIONS.md](CONTRIBUTIONS.md) for generic discovery, shared data
contracts, and resource cards in existing `chat.message_attachment` components.
The SDK contract is available; discovery/proxy endpoints require server support.

### Native mobile surfaces

An app advertises each native surface in `apteva.yaml`:

```yaml
provides:
  ui_surfaces:
    - id: files
      label: Files
      icon: folder
      schema: apteva-native-surface/v1
      entry: /ui/surfaces/files.json
      slots: [mobile.project_app]
```

`entry` is served from the app's existing `ui/` directory. The document is
strict JSON conforming to
[`schemas/apteva-native-surface-v1.schema.json`](schemas/apteva-native-surface-v1.schema.json).
Use `ParseNativeSurface` while developing or testing an app; clients validate
the same contract after downloading it.
See [`NATIVE_SURFACES.md`](NATIVE_SURFACES.md) for the complete host and app
contract. The JSON Schema is normative; the Go validator adds cross-reference
and security rules.

The v1 security boundary is deliberate:

- Surface documents contain no JavaScript, HTML, arbitrary expressions,
  credentials, headers, colors, fonts, or platform-specific symbols.
- API paths are relative to the declaring app. Absolute origins and path
  traversal are rejected.
- Apps do not declare `project_id`; the native host adds the authenticated
  project context to every request.
- Response mappings use only simple JSON selectors such as `$.files` and
  `$.id`. Bindings are limited to `$context`, `$state`, `$item`, `$input`, and
  `$result`.
- Documents are limited to 256 KiB and unknown fields fail closed.

Unsupported surface schema versions should be shown by clients as requiring an
app update. They must not be rendered speculatively.

Dashboard components can add a native renderer alongside their web `entry`:

```yaml
ui_components:
  - name: overview
    entry: /ui/Overview.mjs
    slots: [dashboard.home]
    native:
      schema: apteva-native-surface/v1
      entry: /ui/surfaces/overview.json
```

See the dashboard-widget section in [`NATIVE_SURFACES.md`](NATIVE_SURFACES.md)
for blocks, settings, navigation, and AppBus refresh behavior.

## Talking back to the platform

Use `ctx.PlatformAPI()` — every method is permission-checked server-
side against the manifest's `requires.permissions`:

```go
conn, err := ctx.PlatformAPI().GetConnection(42)
err = ctx.PlatformAPI().SendEvent(instanceID, "process file 1773780")
```

Apps that declare `platform.templates.read` can inspect the portable setup
templates available to a project. A project-scoped context accepts an empty
project ID and pins the request to its current project:

```go
templates := ctx.ProjectTemplatesAPI()
items, err := templates.ListProjectTemplates("", sdk.ProjectTemplateListOptions{
	IncludeSystem: true,
})
definition, err := items[0].DecodeProjectSetup()
```

This capability is read-only. The generic `ProjectTemplate.Definition` keeps
future template kinds forward-compatible; `DecodeProjectSetup` provides the
typed contract for the current `project_setup` kind.

## DB

Declare a `db:` block in the manifest and the framework will:

1. Open the SQLite file at `db.path` inside your app's mounted volume
2. Run `migrations/*.sql` in lexical order, tracked in a `_migrations` table
3. Hand you a serialized writer via `ctx.AppDB()`
4. Hand you a four-connection, query-only pool via `ctx.AppReadDB()`

SQLite permits one writer but concurrent WAL readers. Use `AppDB()` for
transactions and mutations, and `AppReadDB()` for operations that are
guaranteed not to write. The SDK recycles both pools after a live database
replacement; metadata caches can watch `ctx.AppDBGeneration()` for
invalidation.

Cross-app DB access is forbidden. Apps that need to share state expose
MCP tools or HTTP routes; consumers go through them.

## Versioning

The schema is `apteva-app/v1`. Additive fields don't bump the schema;
only breaking changes do. Apps that fail `ValidateManifest` won't boot.

## Local development

```bash
APTEVA_GATEWAY_URL=http://localhost:5280 \
APTEVA_APP_TOKEN=dev-token \
APTEVA_INSTALL_ID=0 \
APTEVA_APP_CONFIG='{"foo":"bar"}' \
go run .
```

### Migration-aware initialization

Apps can declare `runtime.startup_timeout_seconds` (1–3600 seconds; zero uses
60 seconds in the SDK and the caller's existing platform default) and
`runtime.database_upgrade` (`backward_compatible` or `requires_restore`).
These fields require a platform release that implements the startup contract;
Apteva 0.50.1 does not honor the extended deadline.

The SDK listens before opening the database and running `OnMount`. Until both
finish, `/health` returns HTTP 503 with `status: initializing`, `phase`,
`completed`, `total`, and `elapsed_ms`. All other routes return 503. A completed
mount publishes the real routes and `/health` returns HTTP 200 with
`status: ready`. Failed or canceled initialization never publishes app routes.

Use `ctx.StartupContext()` for all mount-time database work. It is canceled on
SIGTERM, SIGINT, or the absolute startup deadline, and is also canceled once
mounting finishes. Do not use it for long-lived workers. Use
`ctx.ReportStartupProgress("tables", completed, total)` to publish progress;
phase names must not contain credentials or customer data. Progress does not
extend the deadline. Apps must cooperate with cancellation; the supervisor
still enforces termination for apps that ignore it.

SQLite initialization uses a one-second writer lock wait and retries atomic SQL
migrations within the remaining deadline. Normal operation restores the
30-second writer wait. Each SQL migration and its receipt commit together;
cancellation rolls back the current migration, not earlier committed files.

`backward_compatible` is an app author's tested compatibility declaration, not a
platform database restore guarantee. Failed activation restores only the prior
process/routing. Automatic activation of `requires_restore` apps is rejected:
those apps need an offline migration and a verified backup/restore procedure.
Do not restore a database underneath a running writer.

### Exclusive process activation

Apps that need an exclusive listener or another process-owned resource can
declare:

```yaml
runtime:
  activation_strategy: exclusive
```

Absent or `auto` retains normal activation. `exclusive` prepares the replacement
while the previous process serves, then stops and reaps that process before
starting the replacement. Either version's policy, or declared fixed host ports,
requires this sequence, so removing the policy still gets one exclusive update.
The scope is one installation; two installations using the same app-configured
port still need distinct configuration. Unknown strategies are rejected, and
static apps cannot declare `exclusive`.

A supporting local supervisor verifies HTTP readiness and known fixed TCP listeners.
Apps must keep `/health` unsuccessful until all required configured or UDP
listeners are ready; binding during `OnMount` uses the SDK's existing startup
gate. Exclusive updates are temporarily unavailable during stop/start and can
interrupt active sessions. Failed activation restarts the previous binary with
its captured configuration and verifies it using its original startup deadline.
Failed rollback is reported as an error. Database restore restrictions still
apply.

Release order: publish a supporting SDK tag, release and deploy the supporting
Apteva server, then release apps that opt in. Set `min_apteva_version` to that
actual server release before an app relies on this field. Support starts in
app SDK `v0.100.0` and Apteva `0.87.4`; use `min_apteva_version: 0.87.4`
for an opting-in app. Older servers may ignore these fields, so upgrade them
before deploying an opting-in app. The supporting server checks `min_apteva_version` at installation and
activation; local `dev` builds validate its syntax without a release comparison.
Manual/remote app mounting does not provide the exclusive supervisor contract.

## User notifications

Apps can opt existing published events into dashboard, desktop and mobile notifications. See [NOTIFICATIONS.md](NOTIFICATIONS.md) for declarations, defaults, audiences, resource follows and delivery semantics.

### Guided setup

Apps can declare feature-based install and edit guidance, including nested
app dependencies and app-owned readiness checks. See [SETUP.md](SETUP.md) for
the manifest contract and CRM/Messaging adoption examples.

### Unified file handles

See [FILE_REFERENCES.md](FILE_REFERENCES.md) for server-stored blobs and app-owned attachments using one handle,
thread grants, internal resolution into binary tool arguments, integration calls,
and capability-based migration from older servers.

## Portable managed deployment packages

SDK v0.101.0 preserves and validates optional `runtime.package` release metadata.
A standard source app uses its Go entry point, declared UI/migrations/skills and
optional `native/` runtime directory. Server inspects the selected backend target
before building; platform, gateway and package delivery are operator-independent.

Declare actual lifecycle semantics once for the release:

```yaml
runtime:
  package:
    app_capabilities:
      passive_start: true
      drain: true
      replica_safe: false
      worker_coordination: false
      routing: key
```

Optional `files`, argv-based `build` steps, `cgo`, `abi` and `system_libraries`
describe exceptions. CGO requires a compatible builder or verified prebuilt
package. No release declaration contains installation configuration or secrets.
Older apps remain supported with conservative lifecycle defaults. A release
with private managed lifecycle routes must declare its capabilities; they are
verified against its authenticated running descriptor before activation.

Runtime backends advertise `package_delivery` with protocol
`apteva.runtime-package/files-v1` and `target_inspection: true`. Their protected
`POST inspect-target` returns `RuntimeTargetInfo` without requiring an artifact.
Supporting Server implementation is currently local/unreleased; these optional
fields do not imply automatic support on older Server versions.

## App UI and agent-setup metadata

SDK v0.105.0 adds optional manifest declarations for:

- [Agent creation recommendations](docs/agent-setup.md): `agent_setup` suggestions with no automatic installation, attachment or permission grants.
- [Widget instance configuration](docs/widget-settings.md): `settings_ui`, scoped dependent option discovery and an opt-in read-only draft preview.
- [Host UI requests](docs/host-ui.md): surface-scoped presentation preferences, initially for floating chat-launcher visibility.

The SDK preserves and validates these declarations. Their host behavior requires
a supporting Apteva Server/dashboard release; support is currently local and
unreleased. Older hosts can ignore the optional metadata.
