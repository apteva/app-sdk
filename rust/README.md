# Experimental Rust app SDK

Local, unpublished first implementation of the [basic app contract](../docs/app-contract.md).
It uses the same manifest, HTTP/MCP routes and binary package format as Go apps.
There is no new Server provider, language field or deployment engine.

Supported: immutable environment/configuration, token authentication, manifest,
health, MCP tool registration/calls, bounded request bodies/connections, SIGTERM
and SIGINT shutdown. Handlers are synchronous and intended for short operations;
validate arguments inside the handler. The initial example performs no network
calls, storage mutations or external side effects.

Not yet supported: managed standby/activation/draining/ownership, components,
databases/migrations, platform client helpers, delegated-user grants, internal
app-call batching, UI routes, resources and event workers. Unsupported managed
execution environment and advanced declarations fail startup. No claim of
remote lifecycle conformance is made. This crate is `publish = false`.

## Build and test

From `app-sdk/rust`:

```sh
cargo build --workspace --locked
cargo test --workspace --locked
cargo clippy --workspace --all-targets --locked -- -D warnings
```

From `app-sdk`:

```sh
GOWORK=off go run ./cmd/apteva-app-contract validate rust/examples/hello/apteva.yaml
GOWORK=off go test ./...
```

Run the example on an unused test port (these are disposable test identities):

```sh
APTEVA_APP_TOKEN=test-token APTEVA_APP_PORT=18080 \
APTEVA_INSTALL_ID=101 APTEVA_PROJECT_ID=test \
APTEVA_APP_CONFIG='{"greeting":"Hello"}' rust/target/debug/apteva-rust-hello
```

From another terminal in `app-sdk`:

```sh
APTEVA_APP_TOKEN=test-token go run ./cmd/apteva-app-contract check \
  --url http://127.0.0.1:18080 --fixtures contract/testdata/hello.json
```

Stop with Ctrl-C. Never use a real installation's token for this example.

## Server compatibility test

From the sibling `server` directory:

```sh
APTEVA_TEST_RUST_APP="$PWD/../app-sdk/rust/target/debug/apteva-rust-hello" \
GOWORK=off go test . -run 'Test(RustArtifactContract|ArtifactManifestWireNumbers|AppArtifact)' -count=1
```

This creates a checksummed archive from the compiled Rust executable, starts it
through Server's real local supervisor, checks the same Go/Rust wire fixtures,
creates a second temporary installation to verify token/configuration isolation,
checks package reuse, failed verification, replacement, stop and restart, and
preserves a persistent-data sentinel. Installation PATH contains no compiler.
All caches/data/ports are temporary. No running Server or existing installation
is contacted. The ordinary test suite skips the Rust binary test unless opted in.

## Release package shape

For a real release, compile with the chosen target toolchain in CI. Build each
supported platform separately; do not label a macOS executable as Linux.
Use the existing archive layout:

```text
bin                         <- compiled executable (mode 0755)
src/apteva.yaml              <- exact release manifest
src/<runtime resources>     <- only explicitly selected files, if any
```

The example manifest uses local `runtime.binaries` placeholders to allow offline
validation before a checksum exists. For publication, choose the release's
approved manifest, package exactly that manifest, and add `runtime.artifacts`
URLs/SHA-256 values to its distribution copy. The artifact map alone may differ
between archive and catalog. No binaries or tarballs are committed here.

`Cargo.lock` pins the example/toolchain dependency graph. The generic Go source
builder is unchanged: installing an artifact does not run Cargo or Go. More
advanced Rust SDK features must pass their corresponding contract tests before
apps declare them.
