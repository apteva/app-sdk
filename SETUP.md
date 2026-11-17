# Guided app setup

`setup` is optional metadata in `apteva.yaml`. It describes what a user wants
from an app and references the existing dependency roles and configuration
schema. The dashboard uses it for install and **Setup & connections** after
installation. It is not a workflow engine, permission grant, or feature toggle.
Turning a setup choice off does not disable tools or remove existing bindings.

## CRM adoption example

Keep CRM's existing `requires.integrations` role `messaging` (`kind: app`,
compatible with Messaging). Add this block to the manifest:

```yaml
setup:
  features:
    - id: contacts
      label: Manage contacts
      description: Store contacts and their activity.
      default: true
    - id: send_email
      label: Send email
      description: Send email directly from contact records.
      requires:
        - role: messaging
          feature: send_email
      fields: [default_sender_email]
      readiness:
        route: /setup/status/email
```

Use the existing `default_sender_email` configuration field; change its input
metadata to fetch verified senders from the **bound installation**, for example:

```yaml
config_schema:
  - name: default_sender_email
    label: Default email sender
    type: select_from_app
    app_role: messaging
    app: messaging
    discovery:
      route: /setup/options/email-senders
      response_path: senders
      value_field: address
      label_field: address
```

`/setup/options/email-senders` is an illustrative app-owned route to implement,
not an existing platform route. It should return only verified email senders
for the authenticated project. Reuse an existing suitable REST route if there
is one. `app_role` is the binding role; `app` provides a name fallback. The
selector uses the exact bound install ID and project, avoiding accidental
lookup against another Messaging installation.

## Messaging adoption example

Messaging declares its own requirements; CRM never duplicates them:

```yaml
setup:
  features:
    - id: send_email
      label: Send email
      requires:
        - role: email_provider
      configure:
        entry: /ui/setup-email
      readiness:
        route: /setup/status/email
```

The example paths are for the app developer to implement and expose through
normal `provides.http_routes` / SDK HTTP routes. The configure entry must be an
actual browser-loadable page; a widget module by itself is not a setup page.
Reuse the app's existing sender/domain verification logic there.

A receive-email feature can reference the existing provider, inbound storage,
and notification roles, with its own configure entry and readiness route.
Only the app knows which combinations are needed for the operation. DNS
publishing, sender creation, test messages, and other writes happen through the
app's explicit UI actions, never during a readiness check.

## Readiness contract

A declared readiness route receives an authenticated **GET** from the server.
The server supplies the install token and signed trusted platform-user
principal, plus the validated project context. Use the SDK's existing trusted
principal/project access mechanisms. A shared app can be checked in a parent
project's context; inspecting or changing its installation settings retains
normal platform permissions.

Return HTTP 200 with `sdk.SetupStatus`, e.g.:

```json
{"status":"needs_setup","message":"Choose a verified email sender."}
```

Allowed statuses: `ready`, `needs_setup`, `pending`, `error`. No secrets in
messages. GET must be read-only and safe to repeat. Non-200 responses, invalid
payloads, timeouts, and unknown statuses are never accepted as ready.

Without a readiness route, satisfying the declared roles/fields produces
**Configured · not verified**, not Ready. Feature fields are required for
readiness; use `config_schema.required` only for requirements that must block
installation regardless of selected features. Checks run only once the app is
running and its structural requirements are satisfied.

Route paths must be absolute app-local paths, without query strings,
fragments, traversal, external hosts, or encoded paths. Configure pages open
through the existing authenticated app proxy with an explicit install ID and
project. Provider OAuth uses the existing integrations connection UI and
returns to the preserved app setup frame.

## Dependency handling

- `requires[].role` references `requires.integrations[].role` or a
  `requires.apps[].name`.
- `requires[].feature` is optional and only valid for app dependencies. It
  names a feature from the dependency's own setup declaration.
- All selected targets in a multi-binding must satisfy the requirement.
- Child setup uses the child's own roles, fields and live checks. Users may
  reuse an installed child or install/configure one while the parent draft
  stays mounted. Returning rechecks the parent.
- A parent's requirement is evaluated even when the child has not saved that
  feature as a preference. Opening the child highlights the required feature;
  saving it is explicit and other saved choices are preserved.
- Missing child features (including older dependency versions), invalid
  bindings, missing required settings, stopped apps, and inaccessible
  dependencies are reported as incomplete.
- Traversal is bounded to 12 levels / 64 evaluated feature nodes, with cycle
  detection, a 12-second overall deadline and 3 seconds per app check.
- Shared installations are labelled and warn that changes may affect other
  consumers. Reuse does not silently overwrite their configuration.

## Persistence and platform API

Existing install, binding, configuration and connection APIs remain the write
paths. Personal credentials are never stored in browser draft persistence.
New endpoints (platform-user authentication and install access required):

- `GET /api/apps/installs/:id/setup`: selected feature IDs and derived status
  tree, with no decrypted configuration values.
- `GET /api/apps/installs/:id/setup?feature=send_email`: also evaluates a parent
  requirement without changing saved choices.
- `PUT /api/apps/installs/:id/setup` with `{"features":["contacts","send_email"]}`:
  saves setup choices only. Normal project editor / global admin access applies.

Choices are stored separately from app configuration. After the first save,
new manifest defaults do not overwrite choices. Removed feature IDs are
filtered when reading old choices. Each install/save is independent; the flow
retains an installed child when a later step fails. Retry resumes configuration
instead of reinstalling that child. It does not roll back shared resources.
Unsaved parent drafts survive dependency navigation and external sign-in in
the current page. Reloading before saving discards those in-memory drafts.
Apps without setup metadata retain the old installer and get generic
role/configuration setup when editing.

## Release order

These fields require an SDK release containing `SetupSpec` and
`ConfigField.AppRole`, and a server/dashboard release containing this flow.
Publish those first, then pin app developers to that SDK tag and update their
minimum server version. No version number is reserved by this change.
For each adopting app, update the manifest, implement its declared routes,
rebuild its runtime manifest/installer artifacts, then release the app.

Recommended app-side regression coverage: incomplete and verified states,
project isolation, exact bound sender lookup, readiness GET having no side
effects, dependency-feature version mismatches, and idempotent setup actions.
