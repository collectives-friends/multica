# Architecture map

## Repository layout

| Path | What lives there |
| --- | --- |
| `server/` | Go backend: HTTP API, WebSocket, daemon, CLI, migrations |
| `packages/core/` | Headless shared logic: API client + zod schemas, query hooks, shared Zustand stores, types, pure utilities |
| `packages/ui/` | UI primitives and shared styles (no business logic, no `@multica/core`) |
| `packages/views/` | Shared web/desktop pages and business components |
| `apps/web/` | Next.js routes/layouts and web-only UI; shared navigation adapters in `apps/web/platform/` |
| `apps/desktop/` | Electron app; renderer in `apps/desktop/src/renderer/src/` (routes, overlays, platform adapters) |
| `apps/mobile/` | Independent Expo/React Native client (own UI, state, hooks, i18n, release) |
| `apps/docs/` | Fumadocs documentation site |
| `apps/ui-lab/` | UI Lab; component documentation powered by the same contracts as `packages/ui/docs/` |
| `e2e/` | Playwright end-to-end tests |
| `scripts/`, `Makefile`, `docker/`, `deploy/` | Local environment, verification, packaging |

## Dependency direction

`views -> core + ui`. `core` and `ui` are independent of each other. Apps stay thin: shared logic moves to `core`, shared UI to `views`. Mobile imports only types and pure utilities from `core`.

## Backend topology (`server/`)

- Entrypoints: `cmd/server` (HTTP server + background workers; router in `cmd/server/router.go`), `cmd/multica` (CLI), `cmd/migrate` (migrations), `cmd/maintenance`.
- `internal/handler/` — one file per domain (`issue.go`, `agent.go`, `comment.go`, `plugin*.go`, ...); handler file names mirror the domain.
- `internal/service/` — cross-cutting business logic (dispatch, attribution, admission, notifications, built-in agents/skills, cron).
- Domain packages: `internal/issuestatus`, `internal/issueproperty`, `internal/issueposition`, `internal/scheduler`, `internal/realtime` (WS hub), `internal/daemon` + `internal/daemonws` (agent runtime), `internal/entitlement`, `internal/featureflags`, `internal/metrics`, `internal/storage`, `internal/analytics`, `internal/maintenance`.
- `internal/integrations/` — channel and VCS adapters: `slack`, `lark`, `dingtalk`, `wecom`, `telegram`, generic `channel`, `github` snapshot, `vcs`, `composio`.
- `pkg/db/queries/` — sqlc source queries; `pkg/db/generated/` — generated `db` package; `pkg/publicapi/v1` — the plugin-facing public API under `/v1`.
- `migrations/` — the schema (see [database.md](database.md)).

## Frontend topology

- `packages/core/api/` — `client.ts`, `schema.ts` (zod), `ws-client.ts`. `packages/core/<domain>/` — hooks and key factories (e.g. `issueKeys.detail(id)`); stores only where client state requires one.
- `packages/views/layout/` — `DashboardGuard`, dashboard shell; `packages/views/<domain>/` mirrors the core domains.
- Navigation is platform-injected: shared code uses `NavigationAdapter`, `useNavigation()`, `<AppLink>`; Next.js and react-router stay in the app layer.
- Desktop specifics: full-window views mount `<DragStrip />` from `@multica/views/platform`; pre-workspace one-shot flows use `WindowOverlay`; cross-workspace navigation goes through `switchWorkspace`.

## Where to look for a feature

Feature slices line up across layers with the same name: `issues`, `agents`, `runtimes`, `inbox`, `chat`, `projects`, `skills`, `squads`, `autopilots`, `plugins`, `labels`, `properties`, `search`, `settings`. Start from the UI route, walk `views -> core -> api -> handler -> queries`, then find the tests at each layer.
