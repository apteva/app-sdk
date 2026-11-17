# Owned app components

An app may optionally declare private, independently deployed services in
`Manifest.Components` / `components` in `apteva.yaml`. For example, an app can
keep its catalog and UI in its main process and send processing jobs to an
`executor` component. Server owns installation identity, placement, credentials,
deployment lifecycle and routing. The operator chooses local execution or a
registered backend; the app does not declare Instances or a cloud integration.

## Declare a worker release

Add this to a valid parent manifest:

```yaml
requires:
  permissions: [platform.apps.call]
components:
  executor:
    protocol: example.executor/v1
    required: true
    capabilities:
      passive_start: true
      drain: true
      replica_safe: false
      worker_coordination: false
      routing: key
    manifest:
      schema: apteva-app/v1
      name: example-worker
      version: 1.0.0
      scopes: [project, global]
      runtime:
        kind: source
        port: 8080
        source:
          repo: https://github.com/example/worker
          ref: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
      provides:
        mcp_tools:
          - name: submit
            description: Submit an idempotent job
```

Go uses `sdk.ComponentDeclarations` and `sdk.ComponentDeclaration`, reusing
`sdk.Manifest` and `sdk.RuntimeAppCapabilities` for the nested worker. YAML and
JSON preserve the declarations through `LoadManifest`, `ParseManifest` and
ordinary serialization. The worker's full manifest is validated normally.

Source references must be exact commits; verified platform artifacts are also
supported. Workers must implement passive start and drain. At most 16 components
are allowed; nested components, public routes, UI/channel/skill/prompt surfaces
and implicit app/integration dependencies are rejected. Give workers scoped job
inputs or signed storage URLs. Configuration, secrets and permissions are not
inherited from the parent. Package lifecycle metadata, when present, must agree
with the component declaration.

`required` describes the app's setup requirements. It does not provision a worker
or block parent startup automatically. Deployment is explicitly approved by an
operator using Server's components/deployment API. The app should expose missing
required services in its own setup/readiness and handle unavailable calls.

## Call from the parent

Use the request's cancellable context, ideally with a deadline:

```go
var result struct {
    JobID string `json:"job_id"`
}
err := app.Components().Call(ctx, "executor", "submit", map[string]any{
    "job_id": jobID,
    "input_url": scopedInputURL,
}, &result)
```

`Call` decodes the normal inner MCP tool result. `CallRaw` preserves the normal
MCP envelope. `CallBatch` accepts existing `sdk.AppCall` and `sdk.AppBatchOptions`
and returns `[]sdk.AppCallResult`; inspect each child result/error as usual.

All methods reuse the existing app-call transport with the alias
`component:executor`. Context cancellation, deadlines and `WithProject` scope are
preserved. There is no new `PlatformClient` method for custom clients or test
stubs to implement. As with other context-aware helpers, a client without
`AppContextClient` support returns an error rather than silently dropping context.

The SDK rejects undeclared names. Server checks the authenticated caller's
ownership, scope, permissions and compatible active deployment. A failed remote
call stays a failure; it does not run locally or implicitly replay an ambiguous
job. The parent needs `platform.apps.call`, which does not grant deployment admin
rights. Durable job IDs, outcomes and safe retry rules remain the app's concern.

## Advertise the worker protocol

Workers can optionally implement:

```go
func (*ExecutorApp) ComponentProtocol() string {
    return "example.executor/v1"
}

var _ sdk.ComponentService = (*ExecutorApp)(nil)
```

`sdk.Run` validates the protocol before startup and mounts
`GET /_apteva/runtime/component/describe`, returning
`{"protocol":"example.executor/v1"}`. The descriptor always requires the worker's
installation token, including in development mode and when broad public routes
exist. Don't also register the same descriptor route yourself. Existing apps
without `ComponentService` gain no routes or startup requirements. Workers with
an existing custom descriptor can keep it and omit the optional interface.

This interface only advertises protocol compatibility. The worker still supplies
its existing managed lifecycle endpoints (`/_apteva/runtime/app/describe`,
`activate`, `drain`) and honors passive start. Server verifies protocol and
capabilities before activating it. Drain must wait for current jobs or confirmed
cancellation before reporting success. The SDK does not infer that health means
a job has finished.

## Compatibility

Use an SDK release containing these types and a Server release supporting owned
components. Older strict manifest parsers reject `components`; do not rely on
them to ignore it. Apps that omit the declaration retain existing behavior.
The workspace's local SDK overlay supports testing before publication; a
published SDK tag is still needed for independently fetched app builds.

Dashboard controls and an app's actual worker extraction are separate from this
SDK contract. Initial Server support permits one private execution per component,
with operator placement, preparation, draining, confirmed stop and rollback.
