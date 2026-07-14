# Apteva App SDK

The public Go SDK for building Apteva Apps — installable units that
extend an Apteva platform with HTTP routes, MCP tools, channels,
prompt fragments, UI panels, and workers.

Apps depend on this module only — never on `apteva-server` internals.

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
- `ui_pages` — iframe-mounted top-level nav entry
- `ui_app` — own subdomain via Traefik (white-label)
- `channels` — inbound + outbound message adapters
- `workers` — background goroutines, cron-style schedule

Anything you don't declare — leave the field out or return `nil` from
the matching `App` method.

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

## Talking back to the platform

Use `ctx.PlatformAPI()` — every method is permission-checked server-
side against the manifest's `requires.permissions`:

```go
conn, err := ctx.PlatformAPI().GetConnection(42)
err = ctx.PlatformAPI().SendEvent(instanceID, "process file 1773780")
```

## DB

Declare a `db:` block in the manifest and the framework will:

1. Open the SQLite file at `db.path` inside your app's mounted volume
2. Run `migrations/*.sql` in lexical order, tracked in a `_migrations` table
3. Hand you the `*sql.DB` via `ctx.AppDB()`

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
