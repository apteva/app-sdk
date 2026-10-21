# Telemetry

`TelemetryClient` is the optional unified API for historical queries, live
subscriptions, resumable delivery, and safe agent/runtime inventory. It leaves
`PlatformClient` and runtime control unchanged. No remote-server registry is needed.

Existing `SubscribeTelemetry(ctx, sub)` calls keep their signature and live-only
content semantics. They delegate to the same `WatchTelemetry` connection and
reconnect implementation, with `LiveOnly: true`. They use the same existing app
SSE endpoint. There is no separate observation API or parallel stream. Old app
binaries continue to receive the original event shape; `WatchTelemetry` opts into
typed delivery frames at that endpoint with `format=delivery`.

Use `ctx.TelemetryAPI()` in an app. For another Server, use
`sdk.NewTelemetryClient(serverURL, apiKey)`. The same typed methods work in
both cases. The remote client uses the credential's normal user/project scope;
it never upgrades a credential to administrator access, follows redirects, or
puts credentials in URLs. HTTP is limited to loopback development.

Permissions:

- `platform.instances.read`: agent inventory and content-free thread/attachment summaries.
- `platform.telemetry.observe`: activity metadata and retained metadata history.
- `platform.telemetry.read`: explicitly requested `content` projection. For apps,
  ordinary agent content remains restricted to the installing user's agents.
- `platform.runtimes.observe`: read-only discovery of other installations'
  runtimes within authorized projects. Runtime content additionally requires
  telemetry content permission and ownership by the installing user.

The Server checks approved installation permissions, active installation status,
installation project scope, and the installing user's current project role.
An authenticated browser acting through an app must also have project access.
Project-scoped apps cannot override their project. Global apps specify a project
for the new read methods. The original live-only content subscription retains its
owned-agent scope across projects for global installations. Runtime control
remains on the original owner-only API.

```go
// app is *sdk.AppCtx; ctx is context.Context.
reader := app.TelemetryAPI()
if reader == nil {
    return errors.New("telemetry SDK support unavailable")
}
stream, err := reader.WatchTelemetry(ctx, sdk.TelemetryWatch{
    Query: sdk.TelemetryQuery{
        Events: []string{"llm.start", "llm.done", "tool.call", "tool.result"},
        Projection: "metadata", // also the default
        Limit: 200,
    },
    Backfill: true,
})
// Handle errors, then consume delivery.Kind:
// history -> delivery.History.Events (bounded newest-ingestion-first page)
// event   -> delivery.Event
// gap     -> indicate incomplete activity; retained history is backfilled
// error   -> the stream ends, e.g. after access is revoked
```

`QueryTelemetry` filters by project, agent, runtime, thread ID/prefix, event types and
inclusive event-time range. Limits are 1–500. Use `NextCursor` with the same
filters for older pages; changing the page size is allowed. History pages use
an ingestion-row boundary, deterministic descending ingestion order and a read
transaction. Queries have a five-second deadline; pages are bounded to about
2 MiB. Content events above 256 KiB preserve their identity and return an
explicit `content_omitted` marker instead of an unbounded payload.

`WatchTelemetry` requires explicit event types. With `Backfill: true`, Server
subscribes before loading a bounded history page, merges the retained live ring,
and backfills again after reconnects or detected replay gaps. The SDK removes
history/live overlap using Server identity plus event ID in a bounded window.
`History.NextCursor` remains available for applications that need older pages.
The SDK owns reconnection/backoff and uses backpressure rather than dropping
frames. A cancellation context closes the stream. `LiveOnly: true` starts fresh on each
connection, for token deltas that should not be replayed. It cannot be combined
with backfill. Original live subscriptions also work against older Servers;
resumable watches require the upgraded Server and fail explicitly on old frames.

Cursors include a Server-process epoch and query fingerprint. `gap` frames
explicitly report `server_restarted`, `retention_exceeded`, or `cursor_reset`.
Keep the cursor from delivered frames if resuming across client lifetimes.
The existing replay ring holds 8,192 events in memory; it is not a durable event
log. Token chunks and other live-only events may be unrecoverable after a gap.
Stored telemetry and live ingestion are separate producer paths: backfill plus
retained replay covers their overlap, but does not promise exactly-once delivery
or recovery of events lost by a producer before Server received them. Apps can
refresh authoritative status after a gap.

Every event uses the same envelope: `server_id`, legacy `instance_id` (Go
`AgentID`), optional `project_id`/`runtime_id`, `thread_id`, `id`, `type`, `time`,
and `data`. Live sequence numbers are process-local, not durable event IDs.
Producer event IDs are preserved. Live events without producer IDs receive a
process-scoped ID and have no durable-history identity guarantee.
`TelemetryStreamEvent.Metadata()` decodes canonical metadata and legacy tool
name aliases. Metadata is an allowlist; unknown payload fields, thoughts,
messages, errors, prompts, arguments and results are excluded.

`ListObservedAgents`, `ListObservedRuntimes`, and `GetObservedRuntime` return
safe typed identities/status. Runtime discovery covers runtimes currently
retained by the manager, not a new archive of destroyed runtimes.
`GetAgentTopology` returns known thread IDs and explicit parents, plus recorded
saved MCP attachments. Tool names are configured filters when known;
`ToolsAvailable: false` means the list is unknown, not empty. No relationships
are inferred from tool-name prefixes. Core unavailability is reported through
`ThreadsAvailable: false`; unavailable attachment inventory uses
`AttachmentsAvailable: false`; it is not invented topology. No private endpoints,
paths, commands, credentials or thread content are returned.
