# Async tool notifications

Tools that return a job ID can ask the server to notify the calling agent's
exact thread through the existing app-event system. This is generic metadata;
the server does not know the app's task lifecycle.

```yaml
provides:
  mcp_tools:
    - name: start_job
      description: Start a job and return its job_id.
      async_result:
        id_field: job_id
        notify:
          target: caller
          mode: stream
          events:
            - job.progress
            - job.input_required
            - job.completed
            - job.failed
            - job.cancelled
          terminal_events:
            - job.completed
            - job.failed
            - job.cancelled
          match:
            job_id: "$result.job_id"
          expires_after: 24h
  publishes:
    - { name: job.progress, description: Job progress changed. }
    - { name: job.input_required, description: The job needs input. }
    - { name: job.completed, description: The job completed. }
    - { name: job.failed, description: The job failed. }
    - { name: job.cancelled, description: The job was cancelled. }
```

## Contract

- Return a JSON object containing the scalar `id_field`, either as MCP
  `structuredContent` or JSON text content. Reference expressions address
  top-level result fields. Match filters must include the returned job ID.
- `mode` defaults to `once`: the first matching event accepted by Core closes
  the subscription. Existing completion-only manifests continue to work.
- `stream` keeps progress/input notifications open. `terminal_events` must be
  a nonempty list of unique, exact topics explicitly included in `events`.
  The app decides which topics are terminal; input requests normally are not.
- `expires_after` is a positive Go duration, defaulting to 24 hours after
  registration. Expiry applies to both new events and queued retries.
- The server binds the subscription to the source installation, effective
  project, caller agent, and caller thread. Notifications never fall back to a
  different thread. A legacy caller without a thread targets `main` explicitly.

Publish each event once using the existing server event API:

```go
err := app.EventBusAPI().PublishAppEvent(projectID, stableEventID, "job.progress", map[string]any{
    "job_id": jobID,
    "message": "Finished collecting the source documents.",
})
```

Persist publication intent with the job update, then retry publication failures
with the same stable event ID and payload. An acknowledged server publication is
durable. Do not send a second direct agent notification for the same event when
using this contract. `PutAppEventSubscription` remains the app-to-app subscription
API; it is not needed for these automatically created agent subscriptions.

## Tool result metadata

The server adds `_apteva_async` with `will_notify`, `subscription_id`, `mode`,
`events`, `terminal_events`, `expires_at`, the returned ID, and instructions.
Registration failure sets `will_notify: false` and an error while preserving the
tool result: the job may already exist, so blindly repeating the tool is unsafe.
The subscription ID is informational and does not grant management authority.

Delivery uses the existing durable outbox with a stable Core event ID on retries.
Events are sent in server acceptance order per subscription. Independent
subscriptions can deliver concurrently. Closing a terminal subscription and
acknowledging its delivery are atomic; later queued progress is discarded.
Success means Core accepted the event, not that the agent finished processing it.

The server journals the existing published event stream for 24 hours and captures
a cursor before calling the tool. Subscription registration atomically replays
matching events since that cursor, covering fast completions before the tool
response. Replay scans at most 10,000 events for that installation/project and buffers at
most 16 MiB of matching payloads;
exceeding this bound or the replay window explicitly fails registration.
Completed registration and queued deliveries survive restarts. A server crash
before the tool response is registered remains an uncertain tool-call outcome;
apps should use their normal job idempotency/status recovery.

Server-mediated thread deletion removes its ephemeral subscriptions and queued
deliveries and invalidates registrations from calls already in flight. Missing
subthreads discovered during delivery are retired instead of recreated. Direct
concurrent Core mutations outside the server cannot be coordinated by this SDK.

## Server compatibility

`PlatformInfo().AsyncResultNotifications` advertises version 1, supported `Modes`,
`Replay`, and `ThreadCleanup`. Missing capabilities mean an older server. Check
for `stream` support before relying on it, and upgrade the server before installing
a manifest with the new field. This change does not migrate an app's existing
direct notifications automatically.
