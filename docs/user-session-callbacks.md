# Browser user context for platform callbacks

Apps installed globally normally call the platform as the installing user. For
HTTP work initiated by a signed-in dashboard user, derive a request context:

```go
requestCtx := mountedCtx.WithUserSession(r)
agents, err := sdk.ListAgentsVia(requestCtx.PlatformAPI(), projectID)
```

This retains only the incoming `session` cookie in a separate context. Callback
requests carry the install's usual bearer token plus `X-Apteva-User-Session`.
The server validates both, preserves install permissions and project boundaries,
and runs user ownership checks as the signed-in user. Invalid, expired, revoked,
or pending-MFA sessions fail with 401; they never fall back to the installer.
The header alone cannot authenticate a request. Requests without a session keep
the existing service identity.

Keep the derived context scoped to this request and work directly caused by it.
Do not replace the mounted context, persist the session, or use it for independent
background workers. `WithProject` and `WithUserSession` compose in either order.
Custom PlatformClient implementations are unchanged; the built-in HTTP client
handles session forwarding. The session is sent only to the callback surface,
not direct cross-app routes. This first implementation handles browser sessions,
not user API-key delegation.

Deploy a server supporting this header before using the SDK method; older servers
ignore it and retain installer identity. The Conversations HTTP accessor now uses
this method so a shared install can create the requesting user's Helper chat.
