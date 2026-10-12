---
name: multica-dev
description: Develop the Multica product itself (multica-ai/multica and its forks). Use when changing product code, adding or changing API endpoints, migrations, sqlc queries, CLI commands, or web/desktop/mobile UI — monorepo layout, Go/Chi backend, PostgreSQL schema rules, workspace-scoped API, WebSocket realtime, and UI conventions. Not for deploying or operating a Multica instance.
metadata:
  audience: agents
  version: "1.0.0"
---

# Developing Multica

A map of the Multica product source. Each reference points to the authoritative files; it does not replace them. When this map and the repository disagree, the repository wins.

## Read first

| You are about to change... | Read |
| --- | --- |
| any product code | `AGENTS.md` (repo root) — package boundaries, state rules, API compatibility, DB/migrations, testing, delivery |
| `apps/mobile/` | `apps/mobile/AGENTS.md` |
| naming, i18n, or Chinese copy | `apps/docs/content/docs/developers/conventions.mdx` |
| the CLI or daemon | `CLI_AND_DAEMON.md` |
| local setup or environments | `CONTRIBUTING.md` + `Makefile` |

## Reference map

| Reference | Covers |
| --- | --- |
| [references/architecture.md](references/architecture.md) | monorepo layout, dependency direction, backend/frontend topology, tracing a change end to end |
| [references/database.md](references/database.md) | schema conventions, migration rules, sqlc workflow, entity map |
| [references/api.md](references/api.md) | HTTP route groups, workspace scoping, response compatibility, WebSocket events |
| [references/cli.md](references/cli.md) | `multica` CLI surface, profiles, daemon behavior |
| [references/ui-ux.md](references/ui-ux.md) | packages, primitives, tokens, i18n, copy rules, desktop/mobile specifics |

## Tracing a change end to end

1. Route: `server/cmd/server/router.go` (chi groups under `/api/...`; plugin public API under `/v1`).
2. Handler: `server/internal/handler/<domain>.go` — resolve UUID-or-human-readable params with the loaders, then write with the resolved ID.
3. Logic: `server/internal/service/` or the domain package (`internal/issue*`, `internal/dispatch`, `internal/scheduler`, ...).
4. SQL: query in `server/pkg/db/queries/<domain>.sql` → `make sqlc` → generated in `server/pkg/db/generated` (`package db`).
5. Wire schema: `packages/core/api/schema.ts` (zod), parsed with `parseWithFallback`; client in `packages/core/api/client.ts`.
6. Query hooks/stores: `packages/core/<domain>/`.
7. UI: `packages/views/<domain>/` for shared pages/components; `apps/web/app/` for web routes; `apps/desktop/src/renderer/src/` for desktop routes/overlays.
8. Realtime: `packages/core/types/events.ts`; feature-owned cache updaters.
9. Tests: Go tests beside the handler; TS tests beside the implementation; E2E in `e2e/`.

## Invariants that prevent most mistakes

- Packages: dependency direction is `views -> core + ui`; `core` and `ui` stay independent; `views` must not import `next/*` or `react-router-dom`, and must not define stores. Mobile shares only types and pure functions from `core`.
- State: TanStack Query owns server data; Zustand owns client state; only auth/workspace stores call `api.*` directly. Workspace-scoped query keys include `wsId`.
- API compatibility: parse UI-consumed JSON with zod + `parseWithFallback`, never `as T`; defaults for optional fields, fallbacks for unknown enums — an installed client may talk to a newer backend.
- Database: no foreign keys, no cascading deletes/updates — clean up in application code. Every index is `CREATE [UNIQUE] INDEX CONCURRENTLY` in its own single-statement migration file; migrations come in `.up.sql` + `.down.sql` pairs.
- HTTP boundary: workspace-scoped handlers filter by `workspace_id`; `X-Workspace-ID` selects the workspace; assignees are polymorphic (`assignee_id` + `assignee_type`).
- Realtime: WS events patch or invalidate Query caches — never write server payloads into Zustand.
- Copy: UI descriptions are optional and omitted by default; state each fact once. Code comments are English.
- Terminology: the tracked unit of work is an **issue**; one agent execution is a **Run** (`task_id` in the API/DB). Keep the two apart in code, copy, and tests.

## Verification

Run the narrowest useful check while iterating, then broaden:

| Scope | Command |
| --- | --- |
| Frontend excluding mobile | `pnpm typecheck`, `pnpm lint`, `pnpm test` |
| Go backend | `make test` |
| End-to-end | `pnpm exec playwright test` |
| Combined web + backend | `make check` |
| Mobile | `pnpm --filter @multica/mobile typecheck` / `lint` / `test` |
| Docs only | link/reference checks + `git diff --check` |

Report what actually ran. Typecheck and unit tests do not prove visual or release behavior.

## Stop and ask

Stop for a product decision before destructive data migrations, expanded access, new paid services, or changes this map does not cover. Do not invent product semantics: check the web/desktop implementation first and match endpoints, permissions, state transitions, and display transforms.
