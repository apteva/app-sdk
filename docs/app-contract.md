# Language-independent app contract

This documents the existing `apteva-app/v1` manifest and HTTP/MCP boundary.
It does not add another transport, controller, endpoint prefix or required
manifest field. Go remains the reference implementation. A compiled executable
can be implemented in any language if it satisfies the selected capabilities.

## Profiles and compatibility

The first executable conformance profile is **basic HTTP + MCP**. A basic app
supports authenticated manifest/tool discovery, configuration, health and
process shutdown. `contract/testdata/basic.json` contains the shared wire
fixtures; `hello.json` adds the example's safe tool. Run both against Go and Rust.
These profile names are test labels, not new fields sent to Server.

Advanced contracts remain independent: app callbacks, events, databases,
UI contributions, scoped callers, owned components, and managed lifecycle.
Do not advertise a feature until implemented. Basic conformance does not certify
an app for managed remote deployment or state migration. In particular, health
is not proof of activation/admission. A missing/failed capability response must
not be treated as permission to start another writable owner.

## Manifest and validation

`schemas/apteva-app-v1.schema.json` covers the structural shape of all current
Go manifest fields, including nested component manifests. Generate it from the
same types with:

```sh
go run ./cmd/apteva-app-contract schema > schemas/apteva-app-v1.schema.json
go run ./cmd/apteva-app-contract validate rust/examples/hello/apteva.yaml
```

The generated schema is for editors and authoring tools. The SDK validator is
still authoritative for semantic rules, defaults, cross-field dependencies,
route safety and capability requirements. This is deliberately not a second
independently maintained implementation of those rules. The schema's `$id` is
an identifier, not a promise that this URL is hosted. Validation uses the
checked-in file; it needs no network.

`validate` writes the normalized JSON manifest. JSON is also accepted as YAML.
Schema drift is a test failure. Existing Go app manifests and runtime behavior
are unchanged. Preserve exact release identity; version and capability changes
must not be hidden behind an old artifact digest.

## Process contract

Server starts the executable as a child process, assigns the HTTP port, and
injects installation configuration. Do not daemonize, choose your own unrelated
port, or put persistent files in the executable/cache directory.

| Environment | Meaning |
| --- | --- |
| `APTEVA_APP_PORT`, `APTEVA_BIND_HOST` | Assigned listener; otherwise manifest values, with loopback as default |
| `APTEVA_APP_CONFIG` | JSON configuration object for this installation |
| `APTEVA_INSTALL_ID`, `APTEVA_PROJECT_ID` | Installation and project identity |
| `APTEVA_DATA_DIR` | Persistent installation directory; preserve across replacement |
| `APTEVA_APP_TOKEN` | Authenticate Server-to-app requests |
| `APTEVA_OUTBOUND_TOKEN` | Prefer this token for app-to-Server calls when supplied; otherwise use app token |
| `APTEVA_GATEWAY_URL` | Server callback base URL; use existing permission-checked APIs |
| `APTEVA_UI_DIR`, `APTEVA_MIGRATIONS_DIR` | Packaged resources, when applicable |
| `APTEVA_DEPLOYMENT_ID`, `APTEVA_DEPLOYMENT_GENERATION`, `APTEVA_RUNTIME_MODE` | Managed execution identity and admission mode; requires lifecycle support |
| `APTEVA_RESOURCE_POLICY` | Existing resource envelope; requires matching resource support |

Do not log configuration or credentials. Server owns permissions, project
binding and placement. An app must not treat user-supplied IDs as authorization.
SIGTERM/SIGINT should stop accepting work and finish cleanup within the host's
shutdown deadline. Process exit must close the listener. Never delete the data
directory during normal shutdown.

## HTTP and MCP

* `GET /health`: unauthenticated, HTTP 200 only when ready, with
  `{"ok":true,"status":"ready"}`. During initialization return non-200. Managed
  standby can be initialized but is not admitted to accept work.
* `GET /manifest`: installation bearer token required; returns the release
  manifest as JSON. Defaults may be omitted; normalized Go serialization is
  not required.
* `POST /mcp`: same token; JSON-RPC 2.0. Implement `initialize`,
  `notifications/initialized`, `tools/list`, and `tools/call` for tool apps.
  Current Go compatibility fixtures negotiate `2024-11-05`. Request IDs must
  survive unchanged. Support both numeric and string IDs.
* Unknown methods return JSON-RPC `-32601`; malformed requests return an error
  without crashing. Match the shared fixtures rather than inventing a new
  envelope. MCP errors may use HTTP 200 with a JSON-RPC error object.
* Tool results use MCP `content`; JSON business data is encoded as a text
  content item. The existing optimized internal app-call response is a separate
  optional contract, not an alternate public MCP endpoint.

Example request:

```json
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"hello","arguments":{"name":"Ada"}}}
```

Authenticate every non-public route. Query strings never bypass authentication.
Public routes/webhooks need an explicit declaration and their own authentication
or signature checks. The experimental Rust implementation has no public route
registration and requires an installation token even in development.

Caller/subject headers are not arbitrary business parameters. The full Go SDK
handles delegated users, signed principals and permission grants. The minimal
Rust SDK accepts ordinary bound-agent calls for tools without declared grants,
but rejects delegated principal and internal bound-app calls. It refuses
permission/resource declarations rather than silently granting access.

## Optional existing contracts

* Platform callbacks: documented types and implementation in `platform.go`.
  Use `APTEVA_GATEWAY_URL` and the outbound token, keep responses bounded and
  respect cancellation. Never expose infrastructure/provider credentials to apps.
* Components: `docs/components.md`, `components.go`. Ownership, routing and
  deployment administration remain Server responsibilities.
* Managed lifecycle: Server uses `GET /_apteva/runtime/app/describe`,
  `GET /_apteva/runtime/app/status`, and `POST /_apteva/runtime/app/activate`
  / `drain`. Activation/drain carry `{deployment_id,generation}` and the
  installation's control authentication. Drain returns `drained:true` only once
  work has actually quiesced. Status, ownership callbacks and admission must
  agree on the exact generation. The authoritative full contract remains
  Server's `docs/app-runtime-backends.md` and lifecycle/ownership code; this
  basic profile does not certify it.

## Delivery and boundaries

Use existing checksummed `runtime.artifacts` packages. The archive contains
`bin` and `src/apteva.yaml`; if `runtime.source.entry` is declared, the manifest
and runtime resources instead live under `src/<entry>/`. `bin` must be executable
on the selected platform. Runtime files such as `ui/` and `migrations/` belong
under the manifest's directory. Installation data and secrets never belong in
archives. Artifact hashes cover archives; remote executable hashes cover `bin`.
Do not substitute one for the other.

The manifest inside the package may omit `runtime.artifacts` to avoid a circular
checksum; all other approved manifest fields must match. A selected artifact
that fails verification must fail installation, never fall back to source.

Server's source builder is still Go-specific. `runtime.package.build` steps
currently precede a Go build; they are not a Rust builder override. Build Rust
in CI and deliver a platform artifact. Native remote packages currently require
Linux ELF and verified ABI/libraries. Python/Node scripts need a supported,
explicit runtime arrangement; an arbitrary script is not a native ELF package.

## Running checks

```sh
# SDK root; test only the explicitly selected disposable app endpoint.
APTEVA_APP_TOKEN=test-token go run ./cmd/apteva-app-contract check \
  --url http://127.0.0.1:18080 --fixtures contract/testdata/hello.json
```

Fixtures execute HTTP requests, including explicitly supplied tool calls. Use
safe test tools and temporary installations. The runner never discovers an
existing Server, installs an app, reads account credentials or follows redirects.
For the Server's isolated artifact/supervisor test, see `rust/README.md`.
