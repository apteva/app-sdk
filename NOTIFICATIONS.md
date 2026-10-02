# App notifications

User notifications reuse `provides.publishes` and the existing app event bus.
Adding `notification` to an event makes that event available under dashboard
Settings → General → Notifications, and from the installed app's Settings tab.
Existing apps and event subscribers remain compatible. An event without this
metadata does not generate user notifications.

```yaml
provides:
  publishes:
    - name: task.assigned
      description: A task was assigned to a person.
      notification:
        id: task-assigned
        name: Task assigned to me
        description: Work assigned to you in this project.
        audience: recipients
        recipients_field: assignee_user_id
        title: "Assigned to you: {title}"
        body: "Due: {due_date}"
        link: "?task_id={task_id}"
        group_by: task_id
        defaults: {in_app: true, tab: true, desktop: false, mobile: false}
        filters:
          - {field: task_id, label: Task ID}
          - field: creator_user_id
            label: Created by
            options:
              - {value: "$me", label: Me}
```

## Event delivery

Publish with the same authenticated app event API:

```go
err := ctx.EventBusAPI().PublishAppEvent(projectID, eventID, "task.assigned", map[string]any{
    "task_id": task.ID,
    "title": task.Title,
    "assignee_user_id": platformUserID,
    "creator_user_id": creatorPlatformUserID,
    "due_date": task.DueDate,
})
```

Use a stable `eventID` for retries. The server acknowledges only after storing
agent/app deliveries and user notifications in the same transaction. Duplicate
IDs with the same topic/project/payload are idempotent; conflicting reuse fails.
A producer needing delivery guarantees should store an outgoing event with its
business transaction and retry this acknowledged API. `ctx.EmitWithProject`
continues to work, but is a best-effort, fire-and-forget producer.

## Audiences and authorization

- `audience: recipients` requires `recipients_field` pointing to one platform
  user ID or an array of platform user IDs. IDs can be JSON numbers or strings.
- `audience: project` means authorized project members/admins with a matching
  subscription. For unscoped global events, only the install owner/admins qualify.
- Use recipients for private resources. A project-wide declaration deliberately
  makes its notification text visible to project subscribers. The server cannot
  infer an app's internal resource ACL or translate its local account IDs.
- The server intersects recipients with current installation/project access at
  creation, inbox access and push delivery. Apps cannot bypass that restriction.
- Sidecar tokens and delegated app identities cannot manage a platform user's
  personal preferences. Use the Web SDK from the user's platform session.
- The producer remains responsible for enforcing its resource access rules.

## Stable types, templates, filters and defaults

Notification IDs are stable within an installation. Do not rename them on each
release. Defaults are snapshotted when a type first becomes available to a user
in a scope; subsequent reads/events do not overwrite saved preferences.
Desktop and mobile defaults must be false. The user enables those channels in
both personal delivery preferences and the desired notification subscription.

Templates interpolate scalar payload fields using `{field}` or `{nested.field}`.
They are plain text. Link templates are app-relative query strings; the server
escapes substituted values and sets the app installation and project itself.
`group_by` names a payload field. The bell groups matching entries and acknowledges
only the event IDs displayed when clicked, preserving newer arrivals.

Each filter is an exact match on a declared payload field. An empty options list
accepts a resource ID; an options list limits allowed values. `$me` resolves to
the authenticated platform user's ID. Multiple filters are ANDed. No executable
expressions or arbitrary payload fields are accepted.

Each user subscription has an installation, project, notification type, channels,
filters and a key. The empty key is the broad type rule. Named keys identify
resource follows. Matching enabled rules combine their delivery channels.
A matching disabled resource rule suppresses the event even if a broad rule
matches. Disabling the broad rule stops broad delivery; explicitly followed
resources remain followed. Personal channel preferences pause all such rules.
Reset removes a rule: the broad rule returns to app defaults; a resource rule
returns to broad-rule behavior. Unsubscribe instead persists an explicit opt-out.

## App UI controls

`@apteva/web-sdk` exposes `client.notifications`:

- `sources(projectId, installId)` lists declarations and effective subscriptions.
- `subscribe(rule)` saves a broad rule or named resource follow.
- `unsubscribe(rule)` saves an explicit opt-out; `reset(rule)` removes an override.
- `preferences()` / `setPreferences(channels)` manage personal delivery switches.
- `list(before?)`, `get(id)`, `markRead(id)`, `dismiss(id)` access the durable inbox.
- `stream(callback)` sends authoritative snapshots, including cross-device read
  state. Reconnect restores the latest snapshot without a process-local cursor.
- `settingsURL(installId)` links to the shared settings UI, scoped to the client
  project and installation. Apps may use this instead of building their own form.

App events still wake agents and feed other apps through their existing
subscriptions. Personal notifications do not create an agent thought.

## Delivery surfaces

In-app bell, tab title, desktop popup, and mobile push are separate channels.
Desktop alerts require browser permission and an open dashboard. Phone delivery
requires a registered device, OS permission, and a configured Push relay.
Push payloads contain the notification ID, project ID and opaque instance reference;
private notification text stays on the originating server. Native clients fetch
`GET /api/notifications/{id}` with their own authenticated session before opening it.

Deployment order: release this SDK, update the server/dashboard, release adopted
apps with the SDK containing these declarations, then update the Push relay and
native clients. Go consumers need the new SDK release for runtime manifest
serialization; merely adding YAML while keeping an older runtime SDK can drop the
new metadata. No registry or remote deployment is performed by a local build.
