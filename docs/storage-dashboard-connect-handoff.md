# Storage integration: dashboard CSP destinations

The server and SDK now support install-scoped registration of dashboard
`connect-src` destinations. Storage has not been changed by this work.

## What Storage should implement

1. Build against an SDK revision containing `DashboardConnectAPI`. Local workspace
   builds use the updated sibling SDK. For independently fetched sidecar builds,
   publish/tag that SDK revision and update Storage's SDK dependency before
   rebuilding Storage; rebuilding the server alone does not update its dependency.
2. Add `platform.dashboard.connect` to `requires.permissions` in Storage's manifest.
   The administrator must approve the new permission when upgrading an existing
   installation. Merely changing the manifest does not grant it. The install must
   be owned by a current platform administrator.
3. Reconcile a stable registration key, such as `storage-backend`, on startup and
   whenever the backend, endpoint, bucket, or browser-facing hostname changes.
4. Register the origin actually used by browser upload requests. For virtual-host
   addressing this includes the bucket, e.g.
   `https://apteva.fsn1.your-objectstorage.com`. For path-style addressing it is
   the endpoint origin. Do not send the bucket path, object key, signed query,
   credentials, or a full presigned URL. Account for a distinct browser/public
   endpoint if the backend uses an internal endpoint for server requests.
5. Clear the registration when switching to disk or disabling direct uploads.
   Keep relay uploads available whenever registration fails or is unsupported.

```yaml
requires:
  permissions:
    # Retain Storage's existing permissions too.
    - platform.dashboard.connect
```

```go
// browserOrigin comes from the backend's actual browser-facing upload target.
// Pass an empty string for disk storage or when direct uploads are disabled.
func reconcileDashboardUploadOrigin(ctx *sdk.AppCtx, browserOrigin string) error {
    api := ctx.DashboardConnectAPI()
    if api == nil {
        return fmt.Errorf("dashboard connection registration is unsupported")
    }
    origins := []string{}
    if browserOrigin != "" {
        origins = append(origins, browserOrigin)
    }
    _, err := api.ReplaceDashboardConnectOrigins("storage-backend", origins)
    return err
}
```

Treat errors as a reason to use the relay and retry reconciliation, not as a reason
for Storage startup or ordinary uploads to fail. A non-nil SDK client only means
that the client implements this feature; an older server can still return an HTTP
error. A successful registration also does not prove that the currently open
browser document has received the policy or that bucket CORS is correct.

## Server contract

The SDK supplies the app install token automatically. No operator API key or
install ID needs to be placed in the browser.

```text
PUT    /api/apps/callback/dashboard-connect-origins/storage-backend
GET    /api/apps/callback/dashboard-connect-origins/storage-backend
DELETE /api/apps/callback/dashboard-connect-origins/storage-backend
GET    /api/apps/callback/dashboard-connect-origins
```

```json
{"origins":["https://apteva.fsn1.your-objectstorage.com"]}
```

PUT replaces the entire named set atomically and is safe to retry. An empty array
clears it. GET/list only exposes the calling install's registrations; DELETE is
idempotent. The server accepts exact HTTPS origins, including explicit ports, and
rejects wildcards or arbitrary CSP directives. Registrations survive restarts and
are removed on uninstall. Permission revocation, disabled/failed installations,
or an owner losing administrator status exclude their grants from new pages.

This changes the **whole dashboard document's** CSP, not just Storage's panel.
CORS, presigned URL authorization, and file-access permissions stay separate and
must still pass. The registry does not configure the bucket.

## Reload and fallback behavior

The server updates the existing dashboard CSP meta tag when it serves HTML from
either disk or its embedded bundle. Other directives and inline-script hashes are
preserved. HTML is `no-store`; hashed assets retain their caching.

Adding or removing destinations requires a full dashboard reload. In-app route
navigation does not refresh the policy. Keep Storage's relay fallback for already
open sessions. Removing one registration does not remove another install's grant
for the same origin and cannot revoke an already loaded document's policy.

## Storage acceptance checks

Server/SDK tests already cover authentication, install isolation, permission
revocation, exact-origin validation, atomic replacement, persistence, uninstall
cleanup, production CSP preservation, HTML caching, and CORS independence.

After wiring Storage, test in a real browser with the production dashboard:

- With valid bucket CORS and the registration present, reload and verify a direct
  multipart upload goes to the bucket rather than the Apteva relay.
- Change the backend and verify the registration replaces the previous origin.
- Remove the registration, reload, and verify direct access is blocked when no
  other registration allows that origin; relay uploads must still work.
- Keep a dashboard open across a registration change and verify fallback behavior.
- Test an older server and an installation without the approved permission.
- Verify existing bucket CORS and Storage file-access checks remain effective.

No Storage upload or credential behavior has been modified or live-tested yet.
