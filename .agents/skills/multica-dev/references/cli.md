# CLI and daemon map

Binary: `server/cmd/multica` (Go). Build with `make build`; run from source with `make cli MULTICA_ARGS="..."`. Full behavior docs: `CLI_AND_DAEMON.md`, `CLI_INSTALL.md`.

## Command surface (one file per domain in `server/cmd/multica/`)

| Area | Commands | Source |
| --- | --- | --- |
| Auth | `login` (browser/OAuth, `--token`), `auth status`, `auth logout` | `cmd_login.go`, `cmd_auth.go` |
| Setup | `setup`, `setup self-host` | `cmd_setup.go` |
| Workspace | `workspace list/switch/get`, `workspace member ...`, `workspace mcp ...` | `cmd_workspace.go` |
| Issues | `issue list/get/create/update/assign/status/reorder`, `issue comment ...`, `issue label`, `issue metadata`, `issue property`, `issue timeline`, `issue wakeup` | `cmd_issue*.go`, `cmd_property.go` |
| Agents | `agent ...` (incl. `agent mcp ...`, copy) | `cmd_agent.go`, `cmd_agent_mcp.go`, `cmd_agent_copy.go` |
| Runtimes | `runtime ...`, `runtime profile ...` | `cmd_runtime.go`, `cmd_runtime_profile.go` |
| Daemon | `daemon start/stop/status/logs`, `daemon disk-usage` | `cmd_daemon.go` |
| Skills | `skill ...` (import, refresh, list, bindings) | `cmd_skill.go`, `cmd_skill_label.go` |
| Other | `project`, `squad`, `label`, `chat`, `autopilot`, `attachment`, `repo`, `user`, `update`, `config`, `version` | matching `cmd_*.go` |

When adding a command: mirror the domain file, keep table output routable (issue `KEY`, short IDs) with `--full-id`/`--output json` escape hatches, and cover it in the matching `_test.go`.

## Profiles and workspace resolution

- Config: `~/.multica/config.json`; named profiles: `~/.multica/profiles/<name>/` (own config, daemon state, health port, workspace root). `--profile <name>` selects one for any command.
- Workspace resolution order (highest first): `--workspace-id` flag, `MULTICA_WORKSPACE_ID` env, then the profile's default (set by `multica workspace switch` or `multica login`).
- Local dev environments isolate CLI profiles under `~/.multica/profiles/dev-<slug>-<offset>/` (see `CONTRIBUTING.md`); do not point experiments at the system profile.

## Daemon

- `make daemon` → `multica daemon restart --profile local`; runs from a built binary, never `go run` (the daemon re-execs its own executable for task helpers).
- Detects agent CLIs on `PATH`, registers one runtime per agent per watched workspace, claims work over WebSocket wake signals with a poll fallback, and runs each task in an isolated workspace directory under `MULTICA_WORKSPACES_ROOT` (default `~/multica_workspaces`).
- Garbage collection policies (task cleanup, artifact reclamation, repo-cache eviction, Hermes memory/session stores, task temp dirs) are documented in `CLI_AND_DAEMON.md` with their `MULTICA_GC_*` knobs.
- Agent-specific overrides (`MULTICA_<AGENT>_PATH`, `_MODEL`, `_ARGS`) are listed per agent in `CLI_AND_DAEMON.md`; agent args apply in order: hardcoded Multica defaults, daemon-wide env defaults, then per-agent `custom_args`.
- Daemon code: `server/internal/daemon/` (runtime detection, task execution, execenv helpers), `server/internal/cli/` (CLI plumbing).
