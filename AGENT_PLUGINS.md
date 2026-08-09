# Agent Plugins 1.0.0 compatibility

Apteva supports the open [Agent Plugins 1.0.0](https://agent-plugins.org/)
package layout without replacing the native Apteva Apps contract.

An app may add these portable files next to `apteva.yaml`:

```text
plugin.json
apteva.yaml
skills/
  crm/
    SKILL.md
mcp.json                 # optional when a portable endpoint/runtime exists
```

The portable manifest points back to the existing Apteva manifest through a
client-owned extension:

```json
{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "crm",
  "version": "0.8.24",
  "extensions": {
    "com.apteva": {
      "manifest": "apteva.yaml"
    }
  }
}
```

Other Agent Plugins clients ignore `com.apteva`. Apteva follows it to the
native manifest, which remains authoritative for:

- app installation and upgrades;
- project/global scope and permissions;
- credentials and integration bindings;
- the authenticated, installation-scoped MCP bridge;
- HTTP routes, UI, data, workers, events, and lifecycle.

This boundary is deliberate. Agent Plugins standardizes discovery of Skills
and MCP configuration, but leaves installation, policy, credentials, and
client-specific capabilities to the client.

## Current host behavior

- Existing `apteva.yaml` URLs behave exactly as before.
- The Apps install and catalog APIs also accept a `plugin.json` URL and resolve
  `extensions.com.apteva.manifest` on the same origin and inside the plugin URL
  directory.
- Source installs discover canonical `skills/*/SKILL.md` files and merge them
  with `provides.skills`. Native declarations win duplicate names, so adding a
  plugin cannot silently alter an established Apteva skill contract.
- Invalid skills and individual MCP entries are isolated and reported. An
  invalid optional plugin never prevents a healthy native app from starting.
- Apteva currently acts as an Agent Plugins Skills client. The SDK validates
  `mcp.json` for portable package authors, while Apteva apps continue to expose
  their tools through the authenticated native app MCP bridge. Do not publish
  a fake portable MCP URL for a project-scoped sidecar.

The CRM app is the reference package. It deliberately ships a portable CRM
Skill but no `mcp.json`: its MCP URL, authorization, project, and installation
identity are assigned by Apteva at install time and are not portable constants.

## Go API

`github.com/apteva/app-sdk/agentplugin` provides:

- `ParseManifest` for the Agent Plugins manifest failure boundary;
- `LoadDir` for fixed-location discovery and component isolation;
- `PrepareStdio` for shell-free `PLUGIN_ROOT` / `PLUGIN_DATA` expansion and
  filesystem containment;
- validated stdio, Streamable HTTP, and legacy SSE MCP entries.

The loader resolves symlinks and rejects files outside the package root,
limits control documents to 256 KiB, requires HTTPS for non-loopback MCP URLs,
and never lets plugin-provided environment variables override host-owned
`PLUGIN_ROOT` or `PLUGIN_DATA`.
