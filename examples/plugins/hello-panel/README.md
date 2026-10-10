# Hello Panel

The reference **manifest v2** plugin: one `issue_panel` with a feature gate,
a live privacy hint, comments, and per-member notes. Other example plugins
remain on manifest v1.

## What it shows

- `beta_panel` defaults on, permits personal overrides, and gates the Hello panel
  with `requires_flag`.
- `show_hint` independently controls the privacy hint. Its live change updates
  the already-open panel through `flags.onChange`; it does not remount the frame.
- `context.flags` follows the same server-resolved snapshot as `flags.get/all`.
- Reading the current issue requires `issues:read`; posting a comment uses
  `comments:write` and is attributed to the signed-in user.
- `storage:user` keeps a separate note for each member in each workspace.
- `ui.resize` adjusts the surface to its content.

The single-file example inlines a small client with the SDK 0.2.0
`context.get` and `flags.get/all/onChange` interfaces. Production plugins can
bundle `@multica/plugin-sdk` instead. Flags are resolved by the server; the
example only renders received booleans.

## Running it

Enable Plugins for your workspace in **Settings → Workspace → Features**.
Zip this folder — the manifest plus every named file — and upload the archive
in **Settings → Plugins**, then install the published version and grant its
scopes. Multica serves its stored artifact; you need no plugin server.

```sh
zip -r hello-panel.zip multica.plugin.json ui
```

`ui/main.js` has no imports. Multica serves one generated document without a
module graph, so bundle dependencies when replacing this inline client.

`MULTICA_PLUGIN_DIR` can publish from disk during development. Each publish
still produces an immutable version; republishing unchanged `2.0.0` becomes
`2.0.0+dev.N`. An installed workspace keeps the version its admin approved
until upgraded.

## Try a live change

1. Open an issue as a member and leave the Hello panel visible.
2. In another browser session, open Features as the workspace admin. Change
   **Private note hint** to **Off by default (members can choose)**. The hint
   disappears inside the existing panel.
3. As the member, switch **My choice** on, then **Reset my choice**. The hint
   follows the personal choice and then the workspace default.
4. As admin, choose **Off for everyone (locked)**. An existing personal-on
   override no longer enables the hint.
5. Set **Hello panel** off. The entire panel disappears without a page reload.

Equivalent administration keys are
`plugin:ai.multica.hello-panel:beta_panel` and
`plugin:ai.multica.hello-panel:show_hint`:

```sh
multica feature list --plugin ai.multica.hello-panel
multica feature set plugin:ai.multica.hello-panel:show_hint off --workspace
multica feature unset plugin:ai.multica.hello-panel:show_hint --workspace
```

The live scenario is covered by `e2e/plugin-feature-flags.spec.ts`. It publishes
and installs this real example in isolated TestApiClient workspaces, opens
separate admin/member browser contexts, and verifies that another workspace
is unchanged. Run against a managed environment with API/web running, `zip`
installed, and Chromium available. Hosted surfaces also need
`MULTICA_PLUGIN_SECRET_KEY` and a dedicated `MULTICA_PLUGIN_SURFACE_ORIGIN`
routed to that environment's API. Do not set a deployment-level
`FF_PLUGINS_V1` when testing workspace-controlled enablement.

```sh
pnpm exec playwright test e2e/plugin-feature-flags.spec.ts
```

The test writes admin/member screenshots and live-change evidence into its
Playwright output directory. It never uses production accounts or data.

## Scope and version boundaries

This plugin declares no `net:` scope, so its surface cannot send requests to
an external service. Its issue/comment/storage actions go through the host
bridge with the signed-in user's permissions and the scopes granted at install.

Manifest v2 snapshots require a compatible backend. After publishing/installing
this version, a v1-only backend is not a safe rollback target. See
[Feature flags and plugins](../../../apps/docs/content/docs/developers/feature-flags.mdx)
for the full precedence and compatibility contract.
