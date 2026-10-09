# Client IP from the platform gateway

HTTP apps can read the client IP resolved by Apteva Server:

```go
ip, err := sdk.ClientIPFromRequest(r)
if err != nil {
    // Invalid platform metadata: do not use it as a client address.
    logger.Warn("invalid client IP metadata", "error", err)
} else if ip != "" {
    // Attach to this request/session's diagnostics.
}
```

The helper verifies signed metadata using the installation's inbound
`APTEVA_APP_TOKEN`. Verification also works on public (`NoAuth`) routes,
independently of user sessions, provider webhook signatures, or call tokens.
Keep all existing authentication and authorization checks.

The server removes incoming `X-Apteva-Client-IP-Metadata`,
`X-Apteva-Client-IP-Signature`, and unsigned `X-Apteva-Client-IP` headers before
adding its own assertion. Assertions use HMAC-SHA256 with a separate signature
domain, expire after one minute, and are bound to the destination method and
request URI. `SetClientIPHeaders` is the platform forwarding helper; apps should
consume `ClientIPFromRequest` rather than minting visitor assertions themselves.

Read the IP before rewriting the app's request URL. For WebSockets, read it
during the HTTP handshake and retain it for that connection. Every reconnect
receives fresh metadata and may have a different address. An established socket
does not need to reverify the expired handshake headers.

Missing metadata returns an empty string and no error, allowing apps to work
with older servers. Forged, incomplete, duplicate, expired, request-mismatched,
or invalid metadata returns an error. The helper does not fall back to
`RemoteAddr`, `X-Forwarded-For`, or `X-Real-IP`. Tokenless development sidecars
and non-app proxy destinations receive no signed assertion.

Server resolution uses the existing trusted proxy configuration. Configure
trusted proxy CIDRs for your deployment and ensure an upstream proxy appends
the actual peer address to its forwarded chain. The legacy trust-all proxy
setting retains its existing behavior; signatures cannot correct an incorrectly
trusted upstream chain.

The address identifies the peer connecting to this ingress. For browser
connections it may identify a VPN exit; for carrier callbacks/media it normally
identifies carrier infrastructure, and for app-to-app proxy calls it identifies
the calling app's connection. It is not a user identity, proof of VPN use, or
evidence that a VPN caused an audio problem.
