# Native surfaces (`apteva-native-surface/v1`)

Native surfaces let an app describe a project-scoped mobile experience without
shipping JavaScript or controlling platform styling. The JSON Schema in
`schemas/apteva-native-surface-v1.schema.json` is the normative document shape.
The Go types and `ValidateNativeSurface` mirror it and add reference, type, and
security checks that JSON Schema cannot express cleanly.

## Discovery and loading

An app advertises a document from its `apteva.yaml` manifest:

```yaml
provides:
  ui_surfaces:
    - id: files
      label: Files
      icon: folder
      schema: apteva-native-surface/v1
      entry: /ui/surfaces/files.json
      slots: [mobile.project_app]
```

The host reads the descriptor from the installed-app API, downloads `entry`
through the declaring app's authenticated proxy, parses it with strict known
fields, and verifies that the document's `id` and `schema` equal the descriptor.
An unsupported schema is an update-required state, not a best-effort render.

## Host responsibilities

The native host owns authentication, project scope, navigation presentation,
loading indicators, errors, accessibility, typography, spacing, colors, glass,
and the mapping from semantic icon names to platform symbols. For every app
request it:

1. Resolves the app-relative path inside the declaring app only.
2. Adds the selected authenticated project context.
3. Applies state and item/input bindings.
4. Executes the request with the user's app permissions.
5. Maps the declared response selector to native components.

Surface documents never provide API origins, authentication headers, cookies,
API keys, CSS, fonts, colors, or platform-specific symbol names.

## State and bindings

State types are `string`, `boolean`, `integer`, `number`, and `string_list`.
Persistence is optional, `session`, or `project`; the default must match the
declared type.

There is no expression language. A whole value may bind to `$context`,
`$state`, `$item`, `$input`, or `$result`, for example `$state.folder`.
Paths may interpolate those namespaces, for example `/files/{item.id}`.
Response mappings are property-only JSON selectors: `$`, `$.files`, or
`$.file.id`. Filters, functions, scripts, and computed expressions are invalid.

## Data sources

A data source declares an HTTP request and response mapping. Methods are
`GET`, `POST`, `PATCH`, and `DELETE`; encodings are JSON or multipart. Request
paths must start with `/`, remain inside the app, and contain no traversal,
query, fragment, or absolute origin. `project_id` is forbidden because the host
injects it.

Responses may map `items`, `item`, `id`, `result`, and `next_cursor`.
Pagination is `none`, `cursor`, or `page`, with a maximum page size of 200.
Refresh policy supports initial loading and pull-to-refresh.

## Native components

V1 sections are:

- `collection`: list, grid, or adaptive records with search, choice/resource
  filters, toggles, badges, destinations, and item actions.
- `metrics`: mapped values rendered using the host's native metric cards.
- `timeline`: mapped event rows from a collection source.
- `properties`: label/value metadata, usually in a detail destination.
- `file_preview`: an app-relative file path plus optional share/delete actions.
- `text`: native text using title and/or description.

Formatting is semantic: `file_size`, `relative_time`, `date_time`, `file_icon`,
`tags`, `number`, and `currency`. Documents do not specify visual formatting.
Each section can declare empty, loading, and error copy; clients choose the
native presentation and must keep retry behavior accessible.

## Navigation, forms, and actions

V1 destinations are native detail pages. They may use the selected collection
item directly or fetch a fresh item through a declared data source.

Action types are `request`, `form`, `file_upload`, `open_url`, `copy_result`,
`share_result`, and `confirm`. Input types are `string`, `multiline`,
`password`, `url`, `integer`, `number`, `boolean`, `choice`, `string_list`,
`file`, and `resource`. A destructive action requires confirmation. A file
upload requires multipart encoding and exactly one required file input.

On success, an action may refresh named sections/data sources, dismiss its
presentation, and show a host-styled toast. Result-based actions select their
value with the same restricted JSON selector vocabulary.

## Validation API

```go
surface, err := sdk.ParseNativeSurface(data)
err = sdk.ValidateNativeSurface(surface)
err = sdk.ValidateNativeSurfaceForDescriptor(surface, descriptor)
```

`ParseNativeSurface` accepts JSON only, rejects unknown fields and trailing
values, enforces a 256 KiB limit, and runs the full validator. Apps should add a
test that the surface document's identity matches its manifest descriptor.

The SDK validates documents but does not fetch them, proxy app traffic, cache
them, or render UI. Those remain platform/client responsibilities.
