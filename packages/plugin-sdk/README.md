# @multica/plugin-sdk

What a Multica plugin surface imports.

```js
import { multica } from "@multica/plugin-sdk";

const ctx   = await multica.context.get();
const issue = await multica.issue.get();
await multica.issue.comment({ body: "hello" });
const note  = await multica.storage.user.get("note");
multica.ui.resize(320);
```

## What a surface is

**One** script in a sandboxed iframe.

Bundle this SDK and everything else your surface needs into a single file. There
is no module graph: you publish an artifact, Multica stores it and serves your
entry inside a generated document on its dedicated plugin-content origin. There
is no path back to the author's server and no top-level module graph for a bare
`import` to resolve against. Publishing refuses an entry that has one rather
than letting it fail later in a reader's browser.

The frame is mounted with `sandbox="allow-scripts"` and **not**
`allow-same-origin`, so it has an opaque origin. Consequences worth knowing
before you write one:

- **No browser storage.** `localStorage`, `sessionStorage` and cookies all throw
  or are empty. Use `multica.storage` — it is server-side, scoped per workspace
  or per member, and survives the frame.
- **`Origin: null` on your own requests.** If your surface calls your backend
  directly, that backend must accept a null origin in CORS.
- **A CSP you did not write.** Multica generates the response and derives
  `connect-src` from the `net:` scopes in your manifest. Declare every host you
  intend to reach; with no `net:` scope your surface cannot issue a network
  request at all, including back to your own origin, which is no longer in the
  policy now that Multica serves your code. `net:` is an exact host, so declare
  `net:api.example.com` separately from `net:example.com`.

## Publishing

Zip the manifest with every file it names and upload it in **Settings →
Plugins**. You need no server of your own for the frontend; hook endpoints and
MCP servers are still yours to run.

A published version is immutable. Installing binds a workspace to one version,
and publishing a new one changes nothing there until an administrator upgrades —
so what they approved on the consent screen is what their browsers run.

## What you can do, and what bounds it

Every call becomes a message to the host, which performs it on the signed-in
user's own session. Two limits apply at once:

1. the scopes the workspace admin granted your plugin, and
2. what that particular user could already do themselves.

So a member without access to an issue gets a 404 through your surface too, and
a scope the admin declined is a 403 that names it. Errors are
`MulticaPluginError` with a `status` mirroring HTTP.

A comment you post is authored by **the user**, recorded as having been made
through your plugin. It does not run `@mention` trigger dispatch — a surface
cannot start agent runs as a side effect of posting text.

## Theme

The host pushes design tokens over the private port and again on every theme switch, and
the SDK writes them as custom properties on `:root`. Use `var(--foreground)`,
`var(--background)`, `var(--border)`, `var(--radius)` and friends and your
surface will look native without shipping a stylesheet.

`multica.ui.onThemeChange(fn)` if you need to react in JS.

## Sizing

The frame does not auto-size. Call `multica.ui.resize(px)` after your content
settles; the host clamps the value.

## Feature flags (0.2.0)

Declare flags in a **manifest version 2** plugin under `contributes.flags`.
Each declaration has a local `key`, `label`, `default`, and `scope_policy`
(`workspace_only` or `member_overridable`). Gate a surface, hook, or resource
with `requires_flag`. An optional flag `parent` references another own flag
or a public host flag as `host:<key>`. Other plugins and private host flags
cannot be referenced.

```js
const showHint = await multica.flags.get("show_hint", false);
const flags = await multica.flags.all();
const context = await multica.context.get(); // context.flags uses the same keys

const unsubscribe = multica.flags.onChange((updated) => {
  renderHint(updated.show_hint ?? false);
});
// Unsubscribe when this view no longer needs updates.
unsubscribe();
```

`get(key, fallback = false)` and `all()` are asynchronous. `all()` returns a
copy of the resolved boolean map. `onChange(listener)` returns an unsubscribe
function and immediately supplies the latest snapshot when one is already
available. A new snapshot updates cached `context.flags` before subscribers
run, including when an event arrives before the first context read.

Only the installation's own short keys and the `host:` keys it references are
included. The server resolves parent, deployment, workspace, and personal
precedence; the SDK does not evaluate rules. Updates arrive over the existing
private MessagePort, without a new action endpoint or credential. There is no
SDK write method for changing workspace or personal overrides.

The bridge protocol stays at version 2; hook wire version stays at 1. Against
an older context with no `flags`, `all()` returns `{}` and `get` returns its
fallback. Version 1 manifests still work but cannot declare the new fields,
even if empty. Once a v2 version or installation snapshot has been stored,
rolling the backend back to a v1-only snapshot reader is unsafe.

[Hello Panel](../../examples/plugins/hello-panel) demonstrates a gated panel
and a separate hint that updates while the panel remains open. See
[Feature flags and plugins](../../apps/docs/content/docs/developers/feature-flags.mdx)
for administration, CLI commands, resolution, and deployment configuration.

### Workspace and project integrations (experimental)

`settings_panel` renders in shared workspace Plugins settings; `workspace_panel`
renders on the workspace dashboard. `project_panel` renders in project details
on web and desktop.
`multica.context.get()` includes `project: { id, title }` when mounted on a project.
These are plugin data surfaces, not native project custom properties.

All three surfaces and the `projects:read`, `projects:write` and
`installation:read` capabilities require the workspace host flag
`plugin_integrations_v1`, which defaults off and is a child of `plugins_v1`.
The host enforces this even when the manifest omits `requires_flag`. Plugins
should also declare their own default-off workspace activation flag whose parent
is `host:plugin_integrations_v1`, and gate each surface and hook on it.

The SDK exposes `multica.projects.list()`, `.get(id)`, `.create(input)` and
`.update(id, input)`. Writes support title, nullable description and project
status only. Each write requires a fresh UUID `operation_id`; updates also require
`expected_revision` from the last read. The server commits a receipt and mutation
in one transaction. Retrying the exact operation returns its original response;
reusing an operation ID with another payload returns 409. Native project edits
increment the same revision, so stale integration updates return 409. Receipts survive project
deletion and host uninstall as immutable history; new installations cannot read
or reuse an old installation’s receipts. Read back
before deciding whether to issue a new operation after an unknown outcome.

Project operations accept a member session through the sandbox bridge or a
standing installation credential. Invocation callback credentials cannot use the
project API. No token enters the iframe.

A backend called through `multica.hooks.invoke()` can authenticate its synchronous
administrative action using `GET /v1/installation/authorization` with the hook's
callback credential. The response includes `credential_kind: "callback"`,
installation/workspace IDs, `hook_key`, the host-captured `input`, and an `actor`
with a currently checked `admin` boolean (owner/admin). Match the input and hook
exactly; never infer administration from the hook actor ID or client claims.
Verify the existing raw-body hook signature and deduplicate `invocation_id` too.
Callback credentials are revoked when the hook returns.

For asynchronous work, use a scoped standing credential to probe this endpoint
before each dispatch. Its response has `credential_kind: "standing"` and
`actor.admin: false`. Require the host and plugin activation flags, and reject
unknown, unreachable, disabled, uninstalled or revoked installations. This probe
is a point-in-time check, not a lease or a cross-system transaction: a backend
must persist its own stop request, fence writers, and drain/read back previously
sent requests before reporting stopped. Keep backend journals, revisions and
tombstones after host uninstall.

Local HTTPS integration tests may use the existing exact-origin
`MULTICA_PLUGIN_DEV_ORIGINS` allowlist with `MULTICA_PLUGIN_DEV_CA`. Public hook
origins continue through the SSRF-protected transport. This experimental source
change does not activate a production integration or change an installation pin.
