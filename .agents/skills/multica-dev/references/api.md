# API map

## Server

- chi router: `server/cmd/server/router.go`; handlers: `server/internal/handler/`.
- Route groups (workspace-scoped unless noted): `/api/workspaces` (+ `/{id}` subresources: members, invitations, agents, runtimes, skills, projects, issues, labels, properties, issue statuses, squads, autopilots, pins, issue views, comments, chat sessions, inbox, dashboard, search index, notification preferences, cloud runtime/billing, share links, workspace MCP), `/api/daemon` (daemon auth), `/v1` (plugin public API, `server/pkg/publicapi/v1`).
- Health: `/health` (identity: `pid`, `commit`, `started_at`), `/readyz`, `/healthz`; WebSocket at `/ws`.
- Workspace scoping: queries filter by `workspace_id`; the `X-Workspace-ID` header selects the workspace (CLI: `--workspace-id` / `MULTICA_WORKSPACE_ID` / profile default); membership gates access. Assignees are polymorphic: read `assignee_id` together with `assignee_type`.
- Handler write rules: resolve UUID-or-human-readable params with the loaders (`loadIssueForUser`, `loadSkillForUser`, `loadAgentForUser`, `requireDaemonRuntimeAccess`) and write with the resolved `entity.ID`; pure UUID input uses `parseUUIDOrBadRequest` and returns immediately when `ok=false`.

## Client (`packages/core/api/`)

- `client.ts` — the single API client. `schema.ts` — zod schemas; UI-consumed JSON passes through a schema with `parseWithFallback`, never `as T`.
- Compatibility: defaults for optional fields; fallbacks for unknown server enums; explicit boolean checks. An installed desktop client may talk to a newer backend — never assume the versions match.
- `ws-client.ts` — WebSocket client; event contract in `packages/core/types/events.ts` (`WSEventType`, `WSEventPayloadMap`). Adding a protocol event means updating both maps and narrowing unknown payloads instead of adding unchecked casts.
- Query keys: feature factories (e.g. `issueKeys.detail(id)`); workspace-scoped keys include `wsId`; account-level keys stay account-scoped.
- Realtime: WS events patch or invalidate Query caches — never write server payloads into Zustand.

## Endpoint change checklist

1. Handler + service behavior, then the sqlc query (`make sqlc`).
2. Update the zod schema in `packages/core/api/schema.ts` and its malformed-response tests.
3. Wire the query hook/mutation in `packages/core/<domain>/`.
4. Update the web route and the desktop router/overlay.
5. Add or update the canonical test at the right layer: Go handler test, core test, views test, or `e2e/`.
