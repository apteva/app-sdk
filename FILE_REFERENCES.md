# Unified file handles

Uploads, app-owned attachments and tool outputs use one public descriptor:

```json
{"_file":true,"ref":"blobref://<id>","filename":"report.pdf","mimeType":"application/pdf","size":12345}
```

Choose storage based on the app's needs:

- **Server-owned bytes:** `BlobsClient.StoreBlob` stores an upload and grants its
  designated agent/thread access in one operation. The server calculates the checksum.
- **Existing app-owned bytes:** `FileReferencesClient.RegisterFileReference`
  registers immutable metadata; the owning app supplies bytes when requested.

Both produce `FileHandle`, use the same grants/revocation API and resolve to the
existing `_binary` envelope only inside declared file arguments. Core retains
and forwards metadata; it never fetches shared bytes. No model-facing filesystem
or download tool is added. `file_ref` is only the incoming-event wrapper produced
by `handle.ContentPart()`.

## Check support first

Use `PlatformInfo()` and check `FileReferences == sdk.FileReferencesVersion`.
Inspect `FileReferenceDispatch` for the exact supported dispatch paths:

- `app_mcp`: direct agent calls to installed app tools.
- `app_integration_callback`: app backend calls through `FileIntegrationClient`.
- `integration_mcp_thread_header`: server integration dispatch accepts the signed
  agent transport plus `X-Apteva-File-Thread` from trusted runtime transport.
- `integration_mcp`: direct agent calls to integration tools with updated Core.
  Core supplies `X-Apteva-File-Thread` from runtime context on signed local MCP
  endpoints. Older Core versions lack this identity and fail closed for references.
- `server_blobs`: durable server storage for uploads and binary tool outputs.

Old servers omit these fields. Keep your existing upload/download path on those
servers; an SDK upgrade alone does not add platform support.

## Server-owned blobs: one handle for inputs and outputs

Use the optional `sdk.BlobsClient` when the server should own the bytes:

```go
blobs := ctx.PlatformAPI().(sdk.BlobsClient)
handle, err := blobs.StoreBlob(requestContext, sdk.StoreBlobRequest{
    Scope: sdk.FileReferenceScope{ProjectID: projectID, AgentID: agentID, ThreadID: threadID},
    Filename: "report.pdf", MIMEType: "application/pdf", Data: bytes,
})
// Include handle.ContentPart() in an incoming event, or return handle as a tool result.
```

Storage requires `platform.files.references` and an attached, app-owned thread
scope. The server stores bytes and grants the designated thread access atomically.
Repeated uploads of the same bytes and metadata in the same owner/project/source
scope reuse the handle. Context cancellation is propagated.

Use the same `FileReferencesClient.GetFileReference`, `GrantFileReference`,
`RevokeFileReferenceGrant` and `RevokeFileReference` methods for these handles.
There is no parallel blob grant API. Grants to another thread require the same
installation/project and, for server blobs, the same agent owner. Ordinary
agent-produced blobs without an app source remain agent/thread-scoped; apps
cannot claim ownership of them.

The common descriptor is:

```json
{"_file":true,"ref":"blobref://<id>","filename":"report.pdf","mimeType":"application/pdf","size":12345}
```

For an existing app-owned attachment, use `ref.Handle()` to obtain this descriptor.
`handle.ContentPart()` wraps it as `{ "type": "file_ref", "file_ref": handle }`
for an agent event. The wrapper does not introduce a new reference scheme.

Trusted agent calls through the gateway automatically register `_binary` tool
outputs as server blobs before returning their handles to Core. Passing either
the scalar reference or the entire descriptor into a `FileArgumentSchema` input
uses the same resolver. Ordinary integration binary/multipart inputs already
declare file support. Storage survives restarts; access is checked on every use.
Other MCP transports keep their existing core-local handling during migration.

Only metadata belongs in events/prompts. `StoreBlobRequest.Data` is an upload
payload and must not be sent to the model. The server remains responsible for
bytes; Core preserves and forwards shared handles.

## Existing app-owned attachments

1. Declare `platform.files.references` in `requires.permissions`. Existing
   installations must approve the new permission through the normal upgrade
   flow. This does not grant filesystem access.
2. Store immutable attachment versions in your existing storage. Calculate
   SHA-256 from the bytes; retain the filename, MIME type and size.
3. Implement `sdk.FileReferenceSource` on your app:

```go
func (a *App) OpenFileReference(ctx context.Context, req sdk.FileReadRequest) (io.ReadCloser, error) {
    // Look up this exact project + attachment ID + version in your database.
    // Never treat an attachment ID as an arbitrary filesystem path or URL.
    // Check deletion/expiry/access in your app, and honor ctx cancellation.
    return a.attachments.OpenVersion(ctx, req.ProjectID, req.AttachmentID, req.Version)
}
```

The SDK mounts an internal authenticated reader automatically. You do not
add a public HTTP route or MCP tool. Reads require a platform signature and a
short deadline in addition to the installation's authentication. The server
never follows redirects or accepts an app-supplied download URL.

4. Register metadata through the optional SDK extension:

```go
files, ok := ctx.PlatformAPI().(sdk.FileReferencesClient)
if !ok { /* use your legacy path */ }
ref, err := files.RegisterFileReference(requestContext, sdk.RegisterFileReferenceRequest{
    ProjectID: projectID, AttachmentID: attachmentID, Version: version,
    Filename: "invoice.pdf", MIMEType: "application/pdf",
    Size: size, SHA256: sha256Hex,
})
```

Registration is idempotent for `(installation, project, attachment ID, version)`.
Different metadata for that same identity produces `file_version_conflict`.
Use a new version when bytes change. Expiry is optional and immutable.

5. Grant access before placing the reference in a thread:

```go
err = files.GrantFileReference(requestContext, sdk.FileReferenceGrant{
    Ref: ref.Ref, AgentID: agentID, ThreadID: threadID,
})
```

The app must be attached to the agent, and the server's existing
`agent_thread_scopes` record must identify this installation and project as
owner. Create the thread through the existing SDK thread API with an explicit
`project_id`. A reference in text is never authorization. Existing threads that
have no recorded app/project scope cannot receive grants; do not backfill their
ownership from client-submitted IDs. Sharing with another thread owned by this
same app requires a separate grant. Cross-app thread delegation is not in v1.

Send compact metadata to the model, including the reference and an instruction
such as: “For a file argument, pass this reference unchanged. The platform
supplies the bytes.” Supported values are `blobref://...`,
`{"_file_ref":"blobref://..."}`, or the returned `_file` metadata object.
Do not put base64 into prompts.

6. Use `RevokeFileReferenceGrant` to withdraw one thread's access, or
   `RevokeFileReference` to revoke the whole reference. Revocation is permanent
   for that registered version. Deleting/reassigning a server thread scope
   removes its grants. Reference mappings survive server/Core restarts;
   deleted source bytes still produce an explicit error.

## Destination app changes

Mark only file inputs with `sdk.FileArgumentSchema`:

```go
InputSchema: map[string]any{
    "type": "object",
    "properties": map[string]any{
        "file": sdk.FileArgumentSchema("The attachment to upload"),
    },
    "required": []string{"file"},
},
```

The schema adds `x-apteva-file: true`. It also works on nested properties or
array item schemas. The tool continues to receive:

```json
{"_binary":true,"base64":"...","mimeType":"application/pdf","size":123,"filename":"invoice.pdf"}
```

The app still handles that envelope in its existing tool implementation. Other
arguments are not interpreted as file references. The destination must be
attached to the calling agent and accessible in the thread's project.

Agent MCP configurations gain a signed dispatch identity when started or
reconciled by the updated server. Restart/reconcile older running agents before
using the new contract. Existing tool calls and core-local `blobref://` handles continue to work during
migration. New shared handles also use `blobref://`; only the owning store and
lifetime differ. Previously issued `apteva-file://` references remain aliases.

## App backend → integration tools

An app can use a granted reference through the existing integration executor:

```go
executor := ctx.PlatformAPI().(sdk.FileIntegrationClient)
result, err := executor.ExecuteIntegrationToolWithFiles(requestContext,
    connectionID, toolName, map[string]any{"file": ref.Ref},
    sdk.FileReferenceScope{ProjectID: projectID, AgentID: agentID, ThreadID: threadID},
)
```

This requires both `platform.connections.execute` and
`platform.files.references`. The backend supplies its own trusted scope; never
copy a model/user-provided `file_scope` unchecked. The server verifies scope
ownership, the grant, project access, and the existing connection binding rules.
Catalog `body_binary_param` and `multipart_form.file_fields` identify supported
file inputs automatically, including arrays for multipart fields. Usage records
retain compact references, not resolved bytes. Delegated provider tools without
file input declarations reject these references.

## Limits and errors

- Maximum 25 MiB per file and combined per call; at most 16 references.
- Tool output limits count distinct files; text/structured copies share a handle.
  The MCP wire response may use up to 72 MiB to carry both representations.
- Each source read has a 30-second deadline; a resolution batch has 45 seconds.
- An aggregate 256 MiB memory reservation limits estimated memory for expanded inputs and
  buffered app-tool responses; saturated requests fail with a retryable error.
- Every read checks current permissions/grants, expiry and revocation, then
  checks grants again after verifying the downloaded bytes. Installation-associated
  server blobs also require the source app to remain running, attached and
  approved for `platform.files.references`.
- Failure aborts dispatch. There is no fallback to public URLs or guessed paths.
- Upload, registration/get/grant and file-aware integration helpers return `*sdk.FileReferenceError` with a stable
  `Code`. Tool resolution returns an error on the original tool call. The same structured errors are decoded from integration callback failures.

Codes include `file_not_found`, `file_deleted`, `file_revoked`, `file_expired`,
`file_inaccessible`, `file_context_required`, `file_too_large`, `file_changed`,
`file_read_failed`, `file_unavailable`, `file_resolution_busy`,
`unsupported_file_argument`, and `file_version_conflict`.

Source readers may return `FileReferenceError` with `file_deleted`,
`file_expired`, or `file_inaccessible`; internal source error details are not
forwarded. Failed transfers are safe to retry. A retry of the destination tool
still follows that tool's own idempotency contract.

## Core transport

Updated Core sends `X-Apteva-File-Thread` from the active runtime thread on
server-authorized local MCP endpoints. The value never comes from model tool
arguments. The server validates the signed agent URL, grants and destination
project before resolution. Old Core versions without this identity retain their
local blob behavior and cannot resolve shared handles through integration MCP.


## Compatibility and lifecycle

`FileHandle` reads legacy `mime_type` and `apteva-file://` values and normalizes
them to `mimeType` and `blobref://`. `FileReference.Handle()` exposes only the
common descriptor, leaving project, checksum and source-registration metadata
out of model-visible content. Existing core-local blobs retain their own TTL;
only server-backed handles persist across restarts.

Deleting an agent thread through the server removes grants even when that
thread had no app-owned scope record. Recreating the same thread ID grants no
access to its old files. Restarting an existing thread preserves its grants.
