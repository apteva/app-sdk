# Realtime capability verification

`RealtimeSpawnResult.Capabilities` distinguishes three independent observations:

- `granted_tools` / `granted_mcp`, with `grants_verified`: stored thread access grants.
- `connected_mcp` / `registered_tools`: actual connections and available tool schemas in the thread runtime.
- `presented_tools`, with `presentation_verified`: the schema names acknowledged for the active realtime provider session/configuration.

Each list uses `null` for unknown and `[]` for verified empty. `status` is `unknown`, `pending`, `ready`, or `failed`. A thread can have grants without a connected MCP, and registered tools without those tools being presented to the voice model.

`capabilities_verified` is true only for a complete, version-1, ready snapshot with matching active/applied session generations and configuration revisions. Deprecated `effective_tools` and `effective_mcp` are projections of confirmed `presented_tools` and `connected_mcp`; otherwise they are null. They no longer copy stored grants. Existing consumers should use the explicit fields.

Provider readiness alone does not prove booking functionality. Apps must check their required tools:

```go
caps, err := sdk.GetRealtimeThreadCapabilities(ctx.PlatformAPI(), agentID, threadID)
if err != nil {
    // Read failed. Keep the existing session/token and retry the GET later.
    return err
}
missing, err := caps.MissingPresentedTools("check_availability", "create_booking")
if err != nil {
    // Evidence is unknown, pending, failed, or stale. Booking is not verified.
    return err
}
if len(missing) != 0 {
    // Report the missing tools; do not declare booking healthy.
}
```

Use exact provider-visible names, including any namespace. Do not treat a similarly named tool as the requested operation. The helper returns an error when readiness is unverified, so an unknown snapshot cannot be mistaken for an empty missing-tools list.

The read API is an optional `RealtimeCapabilitiesClient` extension, preserving compatibility with existing `PlatformClient` implementations and app stubs. HTTP and project-scoped clients implement it. Runtime clients additionally implement optional `RuntimeRealtimeCapabilitiesClient.GetRuntimeRealtimeThreadCapabilities(runtimeID, agentOrAlias, threadID)`.

Server routes:

- `GET /api/apps/callback/threads/{id}/capabilities?agent_id=N`: same installation/agent scope and `platform.realtime.spawn` permission as realtime operations.
- `GET /api/apps/callback/runtimes/{runtime}/agents/{agent-or-alias}/realtime/{id}/capabilities`: existing runtime ownership/permission checks apply.

These GETs do not start agents, spawn threads, renew audio tokens, or restart sessions. Capability inspection after a successful spawn never changes the spawn into an error and never retries POST. The original single-use audio token is preserved.

## Contract for the separate core implementation

Server queries `GET /threads/{id}/capabilities` on core with the core bearer token. Version 1 has this shape:

```json
{
  "version": 1,
  "id": "voice",
  "status": "ready",
  "grants_verified": true,
  "granted_tools": ["check_availability", "create_booking"],
  "granted_mcp": ["bookings"],
  "connected_mcp": ["bookings"],
  "registered_tools": ["check_availability", "create_booking"],
  "presented_tools": ["check_availability", "create_booking"],
  "presentation_verified": true,
  "session_generation": 2,
  "applied_session_generation": 2,
  "configuration_revision": 4,
  "applied_revision": 4
}
```

Core must provide an internally consistent snapshot, not a mixture of different sessions/revisions. `presentation_verified` means provider acceptance for that active session and revision, not schema generation, a queued update, or a successful socket write. If a provider cannot establish acceptance, report pending/unknown instead of ready. Reconnects, disconnects, rejected updates, and pending configuration changes must invalidate readiness until fresh evidence is available. `error` may carry a diagnostic.

Core should expose only the actual schemas/connections for that thread, not main's inventory or inherited assumptions. Registered/presented names include native/synthetic tools where applicable. The server verifies identity/version and revision completeness but cannot independently prove that a provider received schemas: that evidence belongs in core.

Until core implements this contract, the server falls back to `/threads` for grants only and reports `status: "unknown"`, `presentation_verified: false`, and `capabilities_verified: false`. It does not invent connection/registration/presentation evidence. Reads share a five-second deadline and bounded response size. Unknown protocol versions and mismatched thread IDs also remain unverified.

This change implements only server and SDK behavior. Core integration is a separate change; no core readiness support is claimed yet.
