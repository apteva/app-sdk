# Discoverable app contributions

The SDK defines one discovery catalog for read-only data exports, existing UI
components, and existing native surfaces. Producers keep their own records and
UI. Consumers discover contracts or resource renderers instead of binding to
every app by slug.

**Implementation status:** manifest declarations, Go types, payload parsers,
route helpers, resource matching, and the optional HTTP client are available in
the SDK. Server discovery/proxy/authorization endpoints and host rendering are
follow-up work. Adding these declarations alone does not enable discovery on an
older server. No Conversations or Agent Worlds changes are included.

## Producer declarations

```yaml
provides:
  http_routes:
    - prefix: /exports/
  permissions:
    - name: crm.read
  publishes:
    - name: ticket.created
    - name: ticket.updated
  exports:
    - id: overview
      contract: apteva.summary/v1
      label: CRM overview
      endpoint: /exports/overview
      permissions: [crm.read]
      changes_on: [ticket.created, ticket.updated]
    - id: tickets
      contract: apteva.entities/v1
      label: Tickets
      endpoint: /exports/tickets
      permissions: [crm.read]
      changes_on: [ticket.created, ticket.updated]
  ui_components:
    - name: ticket-card
      entry: /ui/TicketCard.mjs
      slots: [chat.message_attachment]
      resource_types: [crm.ticket]
      permissions: [crm.read]
      props_schema:
        type: object
        properties:
          resource:
            type: object
```

`AppExport` is GET-only. IDs are unique within exports; contribution identity is
`install_id + kind + id`. Contracts use `namespace/name/vN`, with exact version
matching. Custom namespaces are allowed; consumers need their corresponding
parser. Unknown versions must produce a fallback rather than guessed data.

`changes_on` must name existing `provides.publishes` declarations (including
declared dynamic topics). It describes invalidation, not another event system.
Publish through the app's existing platform event path. Server-side consumers
must bridge that same event source to export invalidations; producer code should
not emit an additional notification solely for exports.

## Shared data contracts

`SummaryExport` contains sections, semantic hints, statuses, metrics and links:

```json
{
  "contract": "apteva.summary/v1",
  "revision": "42",
  "updated_at": "2026-10-06T10:00:00Z",
  "sections": [{
    "id": "support",
    "label": "Customer support",
    "kind": "work_queue",
    "status": "attention",
    "metrics": [{"id": "open", "label": "Open tickets", "value": 12}],
    "links": [{"label": "Open inbox", "path": "/inbox"}]
  }]
}
```

`EntitiesExport` contains resource identities and relationships:

```json
{
  "contract": "apteva.entities/v1",
  "revision": "42",
  "items": [{
    "ref": {"resource_type": "crm.ticket", "resource_id": "123"},
    "label": "Delivery question",
    "kind": "ticket",
    "status": "open",
    "links": [{"label": "Open ticket", "path": "/tickets/123"}]
  }],
  "next_cursor": ""
}
```

Snapshots/pages are bounded to 1 MiB and 200 top-level items. Empty collections
are `[]`, not `null`. Entity pages accept `cursor` and `limit` (maximum 200);
an optional exact resource filter uses `resource_type` and `resource_id` together.
Parent/relationship targets may be outside the current page. They remain local
to the producing installation and do not convey access to their target.

Metrics contain JSON scalars. Semantic `kind`, `status`, `format` and `icon`
hints are extensible strings; hosts use a generic presentation for unknown hints.
Icons are semantic names. Labels are plain text, never HTML. `ExportLink.Path`
is navigation through the owning app proxy, may contain an app query, and cannot
set project/installation scope. Actions keep using existing app UI/action routes.

Use `ParseAppExport(data, contract)` for strict shared payload decoding (including
unknown-field and trailing-value rejection), or the typed `Validate()` methods.
Schemas are in `schemas/apteva-summary-v1.schema.json` and
`schemas/apteva-entities-v1.schema.json`; Go validation adds reference uniqueness,
permission cross-references, path checks and byte limits.

## Export handlers

Register an export handler through `HTTPRoutes()`:

```go
route, err := sdk.AppExportRoute(manifest.Provides.Exports[0], a.readOverview)
if err != nil {
    panic(err) // invalid static declaration
}
return []sdk.Route{route}
```

`readOverview` has signature `func(*http.Request) (any, error)`. It must validate
the trusted caller identity, enforce project/record access and return authorized
data. Use `PrincipalFromRequest` for platform-signed end users and the app's
existing authorization policy. Never authorize using resource IDs or project
parameters supplied by the model/browser. Return `ErrExportAccessDenied` for
denial. Service-to-service callers also need producer authorization; possession
of an installation token alone is insufficient record authorization.

The helper requires the correct `APTEVA_APP_TOKEN` even if a broad public route
overlaps it, enforces GET, sets `Cache-Control: no-store`, checks response size
and validates shared contract responses before writing. An unconfigured token
fails closed. Register the endpoint in `provides.http_routes` too. Export reads
must not mutate app state.

## Consumer declarations and API

```yaml
requires:
  permissions: [platform.contributions.read]
  contributions:
    - kind: export
      contract: apteva.summary/v1
      optional: true
    - kind: component
      slot: chat.message_attachment
      optional: true
```

A requirement describes a consentable selector, not an automatic record grant
or installation dependency. The server must show and enforce approved selectors
at install/upgrade and apply producer permissions separately. `optional` allows
the app to operate when no compatible provider exists. Discovery does not cause
auto-installation, including for required selectors; the host should report a
missing required contribution to the operator.

```go
api := ctx.ContributionsAPI()
if api == nil {
    return sdk.ErrContributionsUnsupported
}
page, err := api.DiscoverContributions(request.Context(), sdk.ContributionQuery{
    ProjectID: ctx.CurrentProject(),
    Kind: sdk.ContributionKindExport,
    Contract: sdk.SummaryExportContract,
    Limit: 100,
})
// Handle err, then choose an accessible provider from page.Items.
var summary sdk.SummaryExport
err = api.ReadAppExport(request.Context(), sdk.AppExportReadRequest{
    ProjectID: ctx.CurrentProject(),
    InstallID: provider.InstallID,
    ExportID: provider.ID,
}, &summary)
```

The optional `ContributionClient` does not extend `PlatformClient`. Existing
apps and mocks remain source-compatible. Project-scoped contexts default the
project and reject cross-project overrides. User-session clients preserve the
existing delegated session. Context deadlines, response limits and redirect
refusal apply to both HTTP operations.

The client reports `ErrContributionsUnsupported`, `ErrContributionAccessDenied`
or `ErrContributionUnavailable` without retrying or silently falling back to
unrestricted `CallApp`. A nil API indicates a custom client lacks the interface;
an HTTP client still needs a compatible server. Use discovery to detect older
servers (404/501), rather than interpreting a missing export as platform support.

## Resource cards inside chat

Tool results/messages can carry `AppResourceRef`:

```json
{"app_install_id": 42, "resource_type": "crm.ticket", "resource_id": "123"}
```

This is a locator, never an access token. The host validates origin against the
authenticated tool/message provenance, authorizes the referenced record, and
discovers the renderer for that installation, type and slot. It must not trust a
model-supplied installation ID as proof of origin. Consumers attach the trusted
installation to local export refs with `ExportEntityRef.InInstallation`.

`ResourceRenderers(authorizedCatalog, ref, slot)` returns all matches. No match
means a plain-text/link fallback; multiple matches require a host/user choice.
The matcher checks origin installation and slot, but is not an authorization
filter. Hosts pass the validated reference in the component's `resource` prop,
alongside their existing trusted installation/project context. Components fetch
authorized live data and handle deleted or inaccessible records without exposing
cached private content. Mutations use existing action permissions/approvals.

The existing component name, entry, slots, props attachments and widget layouts
remain unchanged. The resource matching fields are optional. Native renderers
keep their existing supported slots; this addition does not expand native chat
rendering or permit JavaScript execution in native clients.

## Server/host protocol to implement next

- `GET /api/apps/callback/contributions`: `project_id`, optional `kind`,
  `contract`, `resource_type`, `slot`, `cursor`, `limit`. Response:
  `{items: [AppContribution], next_cursor?: string}`.
- `POST /api/apps/callback/contributions/exports/read`: `AppExportReadRequest`.
  Resolve the installed declaration; internally GET its endpoint with pagination
  and exact-resource filters. Return the contract payload directly.
- Enumerate only accessible active installations. Handle project/global scope,
  approved consumer selectors, producer permissions, component visibility,
  record-level grants and delegated users. Check authorization on every read and
  action. Catalog membership is not an enduring grant.
- Generate catalogs with `ManifestContributions`; invalidate on installation,
  upgrade, disable, re-enable or uninstall. Match versions exactly. No endpoint
  URL, credential, or authorization decision comes from a consumer request.
- Invalidate affected export snapshots using the existing app event bus. Carry
  source installation/project identity, refresh after reconnect and coalesce
  bursts. `revision` is opaque and must not be treated as a numeric ordering.
- Host implementations discover and embed cards through existing component
  loaders with safe fallbacks. Agent Worlds interprets summaries/entities as its
  own buildings/rooms; producers need no world-specific UI.

Server work should cover ownership/permission denial, upgrade catalog refresh,
disabled providers, pagination, event invalidation/reconnect, installation-scoped
renderer matching, and old-server fallback before hosts adopt the protocol.
