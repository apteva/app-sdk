# App-to-app calls

Apps still declare `platform.apps.call` and a dependency/binding to their target.
Batching and direct JSON do not grant additional access. The platform remains
responsible for routing and authorization; query execution belongs to the app.

## Context-aware calls

Use the context received by `Tool.HandlerCtx` or the HTTP request:

```go
err := sdk.CallAppResultContext(ctx, app.PlatformAPI(), "tables", "rows_list",
    map[string]any{"limit": 20}, &rows)
```

Cancellation covers the outgoing request, admission wait, downstream HTTP, and
response reads. Deadlines are carried across HTTP hops. Target tools must use
`HandlerCtx` and honor its context to cancel their own work; legacy handlers
cannot be forcibly interrupted. Existing `CallApp`/`CallAppResult` methods remain
available with their existing client timeouts. Custom platform clients can
implement the optional `AppContextClient`; context helpers never silently fall
back to a noncancelable method.

## Independent parallel batches

```go
results, err := sdk.CallAppBatchContext(ctx, app.PlatformAPI(), "tables",
    []sdk.AppCall{
        {ID: "recent", Tool: "rows_list", Input: map[string]any{"limit": 20}},
        {ID: "count", Tool: "rows_count"},
    }, sdk.AppBatchOptions{
        Execution: sdk.ParallelIndependent,
        Concurrency: 4,
        ResultMode: "json",
    })
if err != nil { return err }
for _, result := range results {
    var value any
    if err := result.Decode(&value); err != nil {
        // Handle this child's error; other children may have succeeded.
    }
}
```

Sequential execution is the default. `parallel_independent` is an explicit
assertion that calls have no dependencies on each other's results or effects.
It defaults to four workers; the maximum is eight. Each child still passes
through admission control and is charged separately. Results retain input order,
including individual HTTP, protocol, and tool errors. Cancellation stops issuing
remaining calls; it cannot roll back work already performed. There are no
automatic retries and batches are not transactions.

Limits: 32 calls, one target and project, 8 MiB request, 16 MiB per downstream
response, and 32 MiB aggregate response. Oversized children produce individual
errors; exhausting the aggregate budget cancels outstanding dispatches and
fails the outer request. Authorization is evaluated for the outer request;
revocations apply to subsequent requests, not rollback of already-admitted work.

## Wire compatibility

`CallApp` and default batch results retain the raw MCP contract. `CallAppResult`
negotiates `X-Apteva-App-Result-Format: json-v1`. Updated sidecars encode successful
tool results directly as JSON and acknowledge that header; the platform forwards
the bytes unchanged. Protocol/tool errors remain MCP errors. Batch results label
direct data with `format: "json"`; `AppCallResult.Decode` handles either format.

Old clients receive MCP from new sidecars. New clients accept MCP from old
sidecars/servers. Servers without batch support return an HTTP capability error;
servers with the earlier sequential-only batch endpoint may ignore new options
and execute sequentially. No speculative duplicate request is used to detect
capabilities. Deploy the updated server before depending on parallel throughput.

Use `go test -run '^$' -bench BenchmarkAppResultEncoding -benchmem` for a source
encoding microbenchmark. This does not measure gateway latency, database query
execution, or GraphQL end-to-end throughput. Compare those independently before
replacing an app-specific batch/query API with generic parallel dispatch.
