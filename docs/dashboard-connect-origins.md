# Dashboard connection destinations

Apps can register exact HTTPS origins that the dashboard may connect to through
its Content Security Policy (`connect-src`). This supports direct browser uploads
and other browser requests to app-configured services.

This is separate from CORS, backend network access, file permissions, and API
authorization. It extends the **entire dashboard document**, not just one app's
panel. Use the CORS API separately when configuring inbound app requests.

Declare the permission in the app manifest:

```yaml
requires:
  permissions:
    - platform.dashboard.connect
```

The installation must be owned by a current platform administrator and have this
permission in its approved permission snapshot. A manifest update alone does not
grant it. Pending installs can register while starting; disabled or failed
installs do not contribute destinations.

Reconcile on startup and whenever backend configuration changes:

```go
func reconcileDashboardDestination(ctx *sdk.AppCtx, origin string) error {
    api := ctx.DashboardConnectAPI()
    if api == nil {
        return fmt.Errorf("dashboard connection registration is unsupported")
    }
    _, err := api.ReplaceDashboardConnectOrigins("storage-backend", []string{origin})
    return err
}
```

Pass the actual browser-facing origin, for example
`https://apteva.fsn1.your-objectstorage.com`, including the bucket hostname when
virtual-host addressing is used. Do not pass a presigned URL, path, query string,
credential, wildcard, or CSP directive. Explicit HTTPS ports are supported;
`:443` and a trailing `/` normalize away. Each registration accepts at most 100
origins. Names are scoped to the install, so two apps can both use `backend`.

The optional `DashboardConnectClient` exposes:

- `ReplaceDashboardConnectOrigins(key, origins)` — atomically replace the set;
  retrying is safe. An empty set clears it.
- `GetDashboardConnectOrigins(key)` — fetch a named set; absent keys return an error.
- `ListDashboardConnectOriginRegistrations()` — list this install's sets.
- `DeleteDashboardConnectOrigins(key)` — clear a set, including an absent set.

The HTTP client automatically authenticates with the app install token:

```text
GET    /api/apps/callback/dashboard-connect-origins
GET    /api/apps/callback/dashboard-connect-origins/:key
PUT    /api/apps/callback/dashboard-connect-origins/:key
DELETE /api/apps/callback/dashboard-connect-origins/:key
```

PUT body and successful response:

```json
{"origins":["https://bucket.example"]}
```

```json
{"key":"storage-backend","origins":["https://bucket.example"]}
```

Registrations persist across server restarts and disappear on uninstall. A grant
stops contributing when the approved permission is revoked, the installing user
loses administrator status, or the install is disabled/failed. Another install
registering the same origin keeps that destination allowed.

The server updates the existing CSP meta tag when serving dashboard HTML from
disk or the embedded bundle, retaining all other directives and script hashes.
HTML uses `Cache-Control: no-store`; hashed assets remain cacheable. Registrations
only take effect in newly loaded documents: adding **and removing** destinations
requires a dashboard reload. They cannot revoke access from an already open page.

Keep relay uploads available for open pages with an older policy and for servers
without this endpoint. A non-nil SDK client does not prove server support: older
servers return an HTTP error from registration. Bucket CORS and file/API access
checks must still succeed independently. This API does not configure the bucket.
