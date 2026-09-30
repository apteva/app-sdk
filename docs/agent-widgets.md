# Agent overview widgets

Agent detail pages have Personal, Business, and Developer view presets. Each
uses the existing `dashboard.agent_detail` contribution slot and component
loader. The active preset follows the interface preference in Settings; there
is no per-agent view selector. It is a presentation preference, not an
authorization boundary or an agent execution setting.

## Manifest

```yaml
provides:
  ui_components:
    - name: agent-work
      entry: /ui/AgentWork.mjs
      label: Current work
      description: Work assigned to this agent in this app
      slots: [dashboard.agent_detail]
      visibility: attached
      suggested: true
      recommended_views: [personal, business]
      supported_sizes: [half, full]
      default_size: half
      refresh_topics: [work.updated]
```

`recommended_views` is optional and accepts `personal`, `business`, and
`developer`. With no recommendation, a suggested component is eligible for
all presets. Recommendations determine initial placement and the gallery's
recommendation badge; users can add any eligible widget in any view.
Existing manifests remain compatible. The SDK rejects unknown and duplicate
view values. Integrations can declare the same optional field on UI components.

The existing props are retained: `agentId`, `instanceId`, `slot`, `widgetId`,
`widgetSize`, and `widgetSettings`, plus the app event refresh context. Filter
app data using the supplied agent context and enforce authorization in the
app/server. Dashboard eligibility continues to be resolved by the server.

## Layouts

Layouts use the existing user UI preference store and per-surface updates:

- `agent.<id>.<view>.overview`: ordered widget instances, sizes, and settings.

Layouts are nested under the current project in the authenticated user's layout.
A new agent/view layout inherits existing project `dashboard.agent_detail`
widgets, alongside its built-in preset. Once customized, it has its own saved
layout. Existing project preferences are not rewritten. Unknown or temporarily
unavailable widgets are retained in saved layouts. Reset view restores the
preset and inherited project widgets for that view only.

The gallery combines built-in and eligible app widgets. Desktop supports drag
ordering; mobile provides move buttons and a single-column canvas. Each widget
must adapt to its available width. Half/full are layout hints, not fixed pixels.
The gallery and settings use the shared accessible modal/sheet component.

Legacy `instance.status` panels appear below the overview canvas. Existing
`instance.tab` panels remain accessible through App panels. Native-client
surface resolution remains a separate capability; this change adds the web
agent overview and does not extend the native Home-only resolution endpoint.

## Content

Use app-owned data for outcomes, artifacts, and business metrics. A successful
tool invocation does not establish a completed business task. Built-in recent
responses show explicit completed response text; runtime reasoning and idle
messages remain available in Diagnostics.
