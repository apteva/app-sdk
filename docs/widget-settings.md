# Widget instance configuration

`UIComponent.settings_schema` remains the source of setting names, types,
defaults, titles and descriptions. Optional `settings_ui` adds presentation and
read-only option discovery. It does not configure an app installation, connect
accounts, grant permissions or change global app settings.

```yaml
provides:
  ui_components:
    - name: analytics-dashboard
      entry: /ui/AnalyticsDashboardWidget.mjs
      label: Analytics
      slots: [dashboard.home]
      settings_schema:
        type: object
        required: [dashboard_id]
        properties:
          dashboard_id:
            type: integer
            title: Dashboard
          show_trends:
            type: boolean
            title: Show trends
            default: true
          max_metrics:
            type: integer
            title: Maximum metrics
            minimum: 1
            maximum: 6
            default: 3
      settings_ui:
        order: [dashboard_id, show_trends, max_metrics]
        advanced: [max_metrics]
        fields:
          dashboard_id:
            discovery:
              route: /widget-options
              query:
                field: dashboard_id
              response_path: options
              value_field: value
              label_field: label
```

The app implements an authenticated, read-only GET route, for example
`GET /widget-options?field=dashboard_id`. Return only accessible choices:

```json
{"options":[{"value":12,"label":"Website overview"}]}
```

The host sends the request through the existing app proxy with the widget
provider's exact `install_id` and current `project_id` when project-scoped.
Global discovery uses the selected global installation. The manifest cannot
redirect discovery to another app or override host-owned scope/auth parameters.
The app must enforce its usual user/resource authorization. Metadata is not an
access grant. Numeric and boolean values retain their types; do not return an
integer ID as a string when its schema type is `integer`.

## Dependent fields

Queries accept scalar literals, or exactly `{setting: <field name>}`:

```yaml
settings_ui:
  order: [app, topic, site]
  fields:
    topic:
      discovery:
        route: /widget-options
        query:
          field: topic
          app: {setting: app}
        response_path: options
        value_field: value
        label_field: label
```

Every referenced name must exist in the widget's `settings_schema`. References
are typed values, not string interpolation. Empty dependencies defer lookup;
`0` and `false` are valid values. An `app` setting here is an app-owned filter,
not a request destination. Lookup restarts when a dependency changes, cancels
superseded requests, and ignores stale responses. An old selection remains in
the draft; if the new response excludes it, the host requires a new selection
or clearing the optional field before saving. Failed/incomplete lookups do not
erase settings. Retry is available. Lookups time out after 15 seconds; widget
lists are bounded to 1,000 options. Provide an appropriately filtered options
endpoint rather than returning full records or an unbounded catalog.

## Optional steps and Advanced fields

```yaml
settings_ui:
  steps:
    - id: data
      title: Choose data
      fields: [app, topic, site]
    - id: display
      title: Display
      fields: [aggregation, window]
  advanced: [interval]
```

Steps are optional; most widgets should use one form. Fields omitted from the
presentation declaration are still shown (on the final step when steps exist).
Advanced fields are shown in a disclosure. Do not also place an Advanced field
in a step. Use schema `title` and `description` for labels and help text. The SDK
rejects unknown/duplicate field references, invalid or duplicate step IDs,
multiple groups for one field, non-local routes, scope overrides and cyclic
query dependencies. Schemas are limited to 128 fields when `settings_ui` is
present, with at most 16 steps.

## Drafts, save and compatibility

Add and Configure share one host dialog. Widgets with no settings retain quick
Add. Other widgets are configured as unsaved drafts and persisted only on Add
or Save through the existing layout API. Cancel, navigation and an installation
change discard the draft. Save errors keep the draft open. Settings are owned
by a widget instance, so two instances can use different selections.

Without `settings_ui`, existing schemas use the same automatic field controls.
The supported validation subset is primitive types, integer/number bounds,
`enum`, `required`, and `additionalProperties: false`; this is not a full JSON
Schema implementation. Object/array fields have JSON input and root-type checks,
not nested-schema validation. Apps must validate values before using them.
Unknown/unavailable widgets and unchanged legacy settings remain portable during
reordering and resizing. New or changed known-widget settings are validated on
the server; preset validation uses the same SDK helper,
`sdk.ValidateWidgetSettings`. No new persistence store or configuration API exists.

## Optional real-data preview

A widget may declare `settings_ui.preview: true` only if it supports read-only
rendering with unsaved settings. The host passes:

```typescript
{
  widgetSettings: draftSettings,
  widgetContext: {
    // Existing scope/agent/thread context is retained.
    configurationPreview: true,
    hostForeground: false,
  },
}
```

Read and render `widgetSettings`. In configuration preview, avoid writes,
automations, message sending and background actions; controls are inert and the
host action bridge is disabled. Do not opt in until these behaviors are safe.
This context does not bypass app authorization. It is separate from the existing
`preview: true` sample-data convention; sample previews keep their meaning.

These additive declarations are available in SDK v0.105.0. Pin that release
or a later supporting version when shipping an app manifest that declares the
field. Server/dashboard support is currently local and unreleased; require a
supporting Apteva release when the configuration flow is essential.
No Analytics-specific routes or behavior are implemented in the host.
