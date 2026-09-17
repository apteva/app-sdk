# Durable app-to-app event subscriptions

`AppCtx.EventBusAPI()` exposes an optional `EventBusClient`. Applications using
subscriptions declare `platform.events.subscribe`. Custom PlatformClient stubs
remain source-compatible and may implement the optional interface separately.

```go
api := ctx.EventBusAPI()
err := api.PutAppEventSubscription(sdk.AppEventSubscription{
    Key: "on-signup", ProjectID: "project-id", SourceInstallID: 41,
    Topic: "customer.signed_up", Revision: 1, Enabled: true,
})
```

Subscriptions are owned by the calling installation, are project-scoped, and
select a specific accessible source install. Use `ListAppEventSources(project)`
for installed manifests' `provides.publishes` metadata. Topics support exact
names and a trailing `.*`. The platform only accepts a higher revision or an
identical request at the existing revision. To pause, PUT a higher revision with
`Enabled:false`. To resume, PUT a further revision. No historical backfill occurs.

Register an SDK handler for `AppBusDeliveryEvent`. Its Event envelope contains
trusted SourceApp, SourceInstallID, ProjectID, and stable DeliveryID. Data holds:

- `event_id`: stable platform publication identity;
- `subscription_key` and `revision`: the listener snapshot;
- `topic`, `occurred_at`, and `data`: the original event content.

Commit the receipt and any business mutation atomically before returning nil.
Return an error for transient storage failures. Transport retry preserves event
and delivery identities. Reject or record stale subscription revisions before
performing new work. Persist pending business work before acknowledgment so it
can resume independently after an app crash.

`PublishAppEvent(projectID, eventID, topic, data)` publishes through the existing
AppBus emit endpoint and waits for durable subscription enqueue acknowledgment.
Keep the publication ID in the producing app's own transactional outbox and
reuse it after lost responses. IDs are scoped to the source installation and
cannot be reused with changed project, topic, or payload. The server retains
receipt identities to deduplicate publication retries across restart. Events
published before a subscription exists are not replayed by repeating their IDs.

Legacy Emit/EmitWithProjectAck publishers remain supported; without a stable
publisher ID, each separate publication is a distinct event. Stream/SSE clients
still use the existing bounded replay buffer. This capability strengthens app
subscription delivery; it does not turn every AppBus stream into a durable log.
