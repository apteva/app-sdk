# Recommendations during agent creation

An app can optionally recommend itself in `apteva.yaml`:

```yaml
agent_setup:
  recommended: true
  reason: "Delegate tasks to other agents and share results."
```

This is presentation metadata for agent setup. Use the existing app
`display_name`, icon and description for identity. `reason` is optional plain
text, limited to 300 Unicode characters. It explains the benefit of attaching
this app; the host renders it as text, never HTML or executable content.
Omitting the block (or setting `recommended: false`) makes the app an ordinary
app-picker entry. Recommendations do not select, install, start, attach, grant
permissions, change project defaults or configure remote peers automatically.

## Host behavior

Dashboard combines these hints with existing template `recommended_apps` in
one compact Recommended apps list and the existing Add apps picker. It removes
already-selected/required apps from suggestions and groups each app once.
Template suggestions sort first, then installed apps, then name. A matching
project installation takes precedence over a shared global installation.
Another project's private installation is never used as a recommendation.

The installed release's metadata wins over registry hints. A registry cannot
make an older installed release recommend itself. A template may independently
suggest any eligible app without changing that app's manifest.

Add selects an installed, running installation. For an uninstalled app, Add
queues it for the existing Review → Create install flow. Remove cancels that
selection. No install occurs when merely opening Setup, viewing the picker,
selecting a recommendation, or navigating through the wizard. Missing setup
permissions, deprecated catalog entries and unavailable installations retain
the normal restrictions. Server still enforces installation and access checks.
The operator's Default for new agents setting and required template apps keep
their existing semantics; recommendation metadata is not an alternative to
those controls. The agent receives only confirmed app selections.

## Publication and compatibility

SDK preserves `agent_setup` in YAML and JSON and validates it during manifest
loading. Server exposes it on installed-app responses and accepts the same
optional object in registry entries:

```json
{"name":"a2a","agent_setup":{"recommended":true,"reason":"Delegate tasks to other agents and share results."}}
```

Registry publishers can copy this projection directly from the release
manifest. Server also obtains the metadata while performing its existing
cached marketplace manifest enrichment; recommendations add no separate
per-app fetch or recommendation API. A successfully resolved manifest takes
precedence over registry metadata, including an explicit or omitted opt-out.
If manifest resolution fails, valid registry metadata remains available.

Old manifests and registries remain supported. Older hosts may ignore the new
block and keep their ordinary app-picker behavior. The declarations are available in SDK v0.105.0. Server/dashboard support is
currently local and unreleased; use a supporting host release for recommendations
to appear. These changes do not
require a new database table or runtime callback.

## Handover example for A2A

Add the YAML block above to A2A's manifest, publish using a supporting SDK, and
upgrade the installation to that release. Test with a template that does not
already require A2A. Confirm the suggestion starts unselected, Add selects the
correct installation (or queues a new one), Remove works, and the agent receives
A2A only after Review → Create. A2A's tools, authentication and peer configuration
keep their existing contracts; the host needs no A2A-specific code.
