# Host presentation requests

Apps may request presentation changes while a UI surface is active. This is
optional host UI metadata, not an agent permission or a saved user preference.
The first supported setting controls the floating chat assistant launcher.
No Core configuration, agent tool, app endpoint, or extra event stream is needed.

## Manifest default

The same optional `host_ui` is supported on each `provides.ui_panels`,
`provides.ui_components`, and `provides.ui_surfaces` entry:

```yaml
provides:
  ui_panels:
    - slot: project.page
      label: Conversations
      icon: chat
      entry: /ui/ConversationsPanel.mjs
      host_ui:
        chat_assistant:
          launcher: hidden_on_compact
```

Values are `auto` (or absent), `hidden_on_compact`, and `hidden`. The SDK rejects
unknown values. `auto` follows the user's normal settings; it never enables a
disabled launcher. `hidden_on_compact` uses the host's responsive mobile layout
breakpoint (Dashboard's `md`, currently below 768 CSS pixels), not device
identification. `hidden` suppresses only the floating button at every width.
Unsupported hosts ignore the metadata. Native surface descriptors preserve it;
native clients implement their own capability before honoring it.

Use a manifest default only when the entire surface wants that behavior. It is
not an app-wide setting and has no effect while another app/view is active.

## Module panels and contributions

Dashboard injects an optional `host` prop into dynamically loaded module panels
and contributions. Types are available as type-only imports from
`@apteva/ui-kit`: `AppHost`, `HostUI`, and `HostUIRequest`. Old apps can ignore the
prop; apps running on old hosts must handle its absence.

For Conversations, keep the manifest on `auto` (or omit `host_ui`) if only the
opened mobile conversation view should suppress the launcher:

```tsx
import { useEffect } from "react";
import type { AppHost } from "@apteva/ui-kit";

function ConversationsPanel({ host }: { host?: AppHost }) {
  // Existing app-owned state; use the actual view/selection variables.
  const conversationOpen = view === "conversation" && !!selectedConversationId;

  useEffect(() => {
    if (!conversationOpen || !host?.capabilities.ui.chatAssistantLauncher) return;
    return host.ui.request({
      chatAssistant: { launcher: "hidden_on_compact" },
    });
  }, [host, conversationOpen]);

  // Render the existing interface normally.
}
```

Call `request` in an effect, not during render. It returns an idempotent release
function. Requests belong to the mounted surface; apps supply no project,
installation, or surface identifiers. The latest unreleased request overrides
that surface's manifest default. Releasing restores the preceding request, or
the default when none remain. A runtime `auto` request can temporarily override a
manifest `hidden`; release returns to `hidden`. At most 32 outstanding requests
are retained per surface. Unknown fields/values are ignored without side effects.

Dashboard allows the foreground project page, selected app tab, or selected pane
on a focused workspace page. Ordinary canvas widgets, previews, chat attachments,
status strips, and settings panels cannot suppress the global launcher. A hidden
ancestor (`hidden`, `inert`, `aria-hidden`, or CSS visibility/display) revokes the
surface request. Focus/pointer selection changes the active workspace pane.
The capability flag is false on ineligible surfaces.

Requests are also revoked on unmount, render failure, navigation, installation
or project change, and loss of foreground eligibility. An old callback cannot
change another surface. Rebind the effect when `host` changes. Apps still own
their internal view transitions: include the relevant view state in effect
dependencies and return the release function.

Suppression does not close the assistant, discard a draft, stop its work, change
user settings, or end a voice session. Settings → Chat assistant has an Open
assistant action opening the same existing dock even when an app hides its
floating button. The action is shown when the assistant is enabled and available.

## Iframe panels

Iframe panels have the same API through the shared adapter:

```ts
import { connectAppHost } from "@apteva/ui-kit";

const host = await connectAppHost(); // exact same-origin parent by default
const release = host.capabilities.ui.chatAssistantLauncher
  ? host.ui.request({ chatAssistant: { launcher: "hidden_on_compact" } })
  : () => {};

// On leaving this internal view:
release();
// On disposing the panel's bridge:
host.dispose();
```

The bounded handshake falls back to unsupported capabilities on older hosts.
For a deliberately cross-origin iframe, supply the known exact `parentOrigin`;
never use `*` or accept an origin supplied by an incoming message. Frame channel
changes revoke old requests. `host.subscribe(listener)` notifies the iframe
when capabilities/channel change so it can release/rebind its current view's
effect. `pagehide` disposes the adapter automatically.

Dashboard accepts only messages from the registered iframe's `contentWindow`
and exact expected origin, with protocol `apteva.host`, version 1, and its
per-load channel. Requests have an opaque bounded request ID and validated
settings; releases must match the same channel and request ID. Frame reload,
unmount and replacement remove all requests. The adapter uses this transport
behind `host.ui.request`; apps should not invent their own window event bridge.
These are advisory UI requests, not a sandbox for trusted module code.

## Compatibility and rollout

Server persists this metadata in the existing stored manifest and returns it
through existing app, contribution, and surface responses. There is no new table
or preference record. Dashboard owns all active-surface and responsive decisions.

These changes are local until released. Before publishing an app manifest using
`host_ui`, pin a released SDK containing the setting (normal latest-tag consumer
policy). Local workspace builds can use the SDK overlay. Request-only apps may
feature-detect the optional host prop and retain ordinary behavior on old hosts.
