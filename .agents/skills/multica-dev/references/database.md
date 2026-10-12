# Database and persistence map

PostgreSQL, accessed through sqlc-generated Go with `pgx/v5`.

## Workflow

| Concern | Location |
| --- | --- |
| Schema | `server/migrations/NNN_descriptive_name.{up,down}.sql` |
| Queries (source) | `server/pkg/db/queries/<domain>.sql` |
| Generated Go | `server/pkg/db/generated/` (`package db`); config `server/sqlc.yaml` |
| Regenerate | `make sqlc` (pinned sqlc version) |
| Apply | `make migrate-up` (or `cd server && go run ./cmd/migrate up`) |
| DB-backed Go tests | `server/internal/testutil` fixtures: `dbfx.Issue`, `dbfx.Task`, `dbfx.Insert`, `testutil.Call(h, req).Want(...).JSON(&out)` |

## Rules (authoritative in root `AGENTS.md` + `conventions.mdx`)

- Tables `snake_case` singular; columns `snake_case`; foreign-key-shaped columns `<table>_id`.
- **No** foreign keys, cascading deletes, or cascading updates. Validate relationships and clean up dependents in application code; use a transaction when the operation must be atomic.
- Every index — including indexes on new tables — uses `CREATE [UNIQUE] INDEX CONCURRENTLY`, and each concurrent index build gets its own single-statement migration file (the runner executes files outside an explicit transaction).
- Always provide both `.up.sql` and `.down.sql`.
- Conditionally skipped migrations are still recorded in `schema_migrations`; later DDL touching conditional objects must be idempotent (`IF EXISTS` / `IF NOT EXISTS`).
- UUID handling in `server/internal/handler/`: resolve UUID-or-human-readable params with the loaders (`loadIssueForUser`, ...) and write with the resolved `entity.ID`; pure UUID input uses `parseUUIDOrBadRequest` and returns immediately on `ok=false`; `parseUUID` only for trusted round-trips; `util.ParseUUID` elsewhere with a checked error.
- State naming prefers `is_<state>` or `<state>_at`.

## Entity map (landmarks, not the full schema)

| Domain | Tables (selection) |
| --- | --- |
| Identity & access | `"user"` (quoted reserved word), `member` (workspace membership + role), `personal_access_token`, `verification_code`, `notification_preference` |
| Workspace | `workspace`, `workspace_invitation`, `workspace_share_link`, `seat_capacity_outbox` |
| Work items | `issue`, `issue_status` (custom workflow statuses with lifecycle categories; SQL function `issue_effective_status`), `issue_property` (custom property definitions; per-issue values live in the issue JSON `properties`), `issue_dependency`, `issue_subscriber`, `issue_view` (+ `issue_view_preference`), `issue_wakeup` (+ receipts), `project`, `project_resource`, `attachment`, `comment` (+ `comment_reaction`), `activity_log`, `search_index_change` |
| Labels | base table `issue_label` (historical name) with join tables `issue_to_label`, `agent_to_label`, `skill_to_label` |
| Agents & executions | `agent`, `agent_runtime`, `agent_skill`, `agent_task_queue` (one agent execution — a **Run**), `task_message`, `task_token`, `task_supplement`, `runtime_profile`, `runtime_usage`, `daemon_connection`, `daemon_token`, `daemon_pairing_session` |
| Collaboration | `chat_session`, `chat_message`, `chat_draft_restore`, `inbox_item`, `pinned_item`, `quick_action`, `squad`, `squad_member` |
| Automation | `autopilot`, `autopilot_trigger`, `autopilot_run`, `autopilot_quota_*`, `webhook_delivery`, `maintenance_job`, `sys_cron_executions` |
| Integrations | `github_installation`, `github_pull_request*`, `vcs_connection`, `vcs_pull_request`, `vcs_commit_status`, channel tables (`channel_*`, `lark_*`, `dingtalk_*`), `workspace_mcp_server`, `user_composio_connection` |
| Skills & plugins | `skill`, `skill_file`, `skill_to_label`, `plugin_*` (identity, installation, release, contribution, grant, binding, invocation, package, storage, secret, remote MCP OAuth) |
| Usage & telemetry | `task_usage*` (+ hourly/dashboard rollups), `client_usage_daily`, `instance_telemetry_state` |

For the authoritative schema read `server/migrations/`; for query shapes read `server/pkg/db/queries/`.

## Terminology

- **Issue** = the tracked unit of work. **Run** = one agent execution (`task_id` in the API/DB). Never collapse the two in code, copy, or tests.
- Issue keys (`MUL-123` style) = workspace `issue_prefix` + sequence number.
