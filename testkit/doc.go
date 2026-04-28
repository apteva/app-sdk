// Package testkit provides helpers for writing tests against Apteva
// apps. Two tiers are supported:
//
//	Tier 1 — in-process tests against the app's tool / route handlers.
//	         Use NewAppCtx to get a fully-wired *sdk.AppCtx backed by
//	         an in-memory SQLite. Migrations from the manifest's db
//	         block are applied automatically. No process boundary, no
//	         HTTP, runs in milliseconds.
//
//	Tier 2 — real-binary tests that spawn the compiled sidecar and
//	         talk to it over JSON-RPC + REST like the platform would.
//	         Use SpawnSidecar.
//
// Tier 3 (live-agent scenarios with apteva-server + apteva-core) is a
// separate concern handled by the `apteva test` CLI subcommand —
// see https://github.com/apteva/apteva.
//
// Public API for any app author. Same import path for first-party
// and third-party apps:
//
//	import tk "github.com/apteva/app-sdk/testkit"
package testkit
