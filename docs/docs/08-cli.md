# 8. CLI Reference

`veriproc` is a thin HTTP client over the daemon's v1 REST API. It keeps **no local
control-plane state**: every state-changing command is an API call. This page documents the
global options, every subcommand, output formats, and exit codes.

## 8.1 Global options

Global flags may appear in any position and are stripped before subcommand parsing.
Precedence is **flags > environment > defaults**.

| Flag | Env | Default | Meaning |
|------|-----|---------|---------|
| `--api-url <url>` | `VERIPROC_API_URL` | `http://localhost:8080` | Daemon base URL. |
| `--token <token>` | `VERIPROC_TOKEN` | — | Bearer token for authenticated daemons. |
| `--output <fmt>`, `-o` | `VERIPROC_OUTPUT` | `table` | `table`, `json`, or `yaml`. |
| `--timeout <dur>` | `VERIPROC_TIMEOUT` | `30s` | HTTP client timeout (Go duration). |

```bash
export VERIPROC_API_URL="http://localhost:8080"
export VERIPROC_OUTPUT="table"
veriproc station list
```

## 8.2 Command summary

| Command | API mapping |
|---------|-------------|
| `submit` | `POST /api/v1/tasks` |
| `task get <task_id>` | `GET /api/v1/tasks/{id}` |
| `task list` | `GET /api/v1/tasks` |
| `task delete <task_id>` | `DELETE /api/v1/tasks/{id}` |
| `run get <run_ref>` | `GET /api/v1/runs/{id}` |
| `run list` | `GET /api/v1/runs` |
| `run jobs <run_ref>` | `GET /api/v1/runs/{id}/jobs` |
| `run delete <run_ref>` | `DELETE /api/v1/runs/{id}` |
| `artifact list --run <run_ref>` | `GET /api/v1/runs/{id}/artifacts` |
| `logs <run_ref>` | `GET /api/v1/runs/{id}/logs` |
| `cancel <run_ref>` | `POST /api/v1/runs/{id}/cancel` |
| `promote <run_ref>` | `POST /api/v1/runs/{id}/promote` |
| `group list` | `GET /api/v1/groups` |
| `group get <id>` | `GET /api/v1/groups/{id}` |
| `group close <id>` | `POST /api/v1/groups/{id}/close` |
| `station list` | `GET /api/v1/stations` |
| `station summary` | `GET /api/v1/stations/summary` |
| `station pause <id>` | `POST /api/v1/stations/{id}/pause` |
| `station unpause <id>` | `POST /api/v1/stations/{id}/unpause` |
| `clean` | `POST /api/v1/maintenance/clean` |
| `health` | `GET /health` |
| `readiness` | `GET /readiness` |
| `version` | local (optional `--check-api`) |

## 8.3 `submit`

Create a task.

| Flag | Required | Meaning |
|------|----------|---------|
| `--station <id>` | yes | Destination station. |
| `--start <RFC3339>` | yes | Window start (e.g. `2025-07-03T11:00:00Z`). |
| `--end <RFC3339>` | yes | Window end. |
| `--force` | no | Force a (non-canonical) replay run. |
| `--priority <p>` | no | Priority hint. |
| `--idempotency-key <k>` | no | Idempotency-Key; equal key + content returns the same task. |
| `--split-group <id>` | no | Associate with a split group. |

```bash
veriproc submit --station scen-clim \
  --start 2025-07-03T11:00:00Z --end 2025-07-03T11:15:00Z
```

## 8.4 `task`

```bash
veriproc task get <task_id>
veriproc task list [--station_id <id>] [--state <state>] \
                   [--split_group_id <id>] [--parent_task_id <id>] \
                   [--limit <n>] [--cursor <c>]
veriproc task delete <task_id> [--dry-run] [--force]
```

- `task list` supports filters `station_id`, `state`, `split_group_id`, `parent_task_id`,
  plus `limit`/`cursor` for pagination.
- `task delete` removes a task and all its runs; `--dry-run` previews, `--force` skips the
  confirmation prompt.

## 8.5 `run`

```bash
veriproc run get <run_ref>          # run_ref = TASK_ID/rN (or internal run id)
veriproc run list [--task_id <id>] [--state <state>] [--canonicality <c>] \
                  [--station_id <id>] [--limit <n>] [--cursor <c>]
veriproc run jobs <run_ref>
veriproc run delete <run_ref> [--dry-run] [--force]
```

`run list` filters: `task_id`, `state`, `canonicality` (`canonical`/`duplicate`/`forced`),
`station_id`, with `limit`/`cursor`.

## 8.6 `artifact` and `logs`

```bash
veriproc artifact list --run <run_ref> [--type <logical_type>]
veriproc logs <run_ref>
```

`artifact list` lists a run's tracked artifacts, optionally filtered by logical type.
`logs` retrieves the run's log artifacts.

## 8.7 `cancel` and `promote`

```bash
veriproc cancel <run_ref> [--reason "<text>"] [--yes]
veriproc promote <run_ref> --reason "<text>"
```

- `cancel` requests cancellation of an active run (`--yes` skips confirmation). Whether the
  job is actually stopped depends on executor support.
- `promote` forces a `duplicate` run to become canonical (reason required), routing it
  downstream. Use when a duplicate's outputs should supersede the original.

## 8.8 `group`

```bash
veriproc group list [--state <state>] [--limit <n>]
veriproc group get <split_group_id>
veriproc group close <split_group_id>
```

Lists/inspects split groups (with `member_count`, `canonical_count`, `failed_count`) and
closes a group to finalize fan-in. See [Operations §11.4](11-operations.md).

## 8.9 `station`

```bash
veriproc station list
veriproc station summary
veriproc station pause <station_id>
veriproc station unpause <station_id>
```

`summary` returns a per-station health overview. `pause`/`unpause` stop or resume admission
of new work for a station.

## 8.10 `clean`

Bulk deletion by time cutoff.

| Flag | Default | Meaning |
|------|---------|---------|
| `--before <ts>` | — | Delete tasks at or before this timestamp. |
| `--after <ts>` | — | Delete tasks at or after this timestamp. |
| `--by <basis>` | `processing-time` | Cutoff basis: `processing-time` (created_at) or `processing-window` (sensing window). |
| `--dry-run` | false | Report what would be deleted without deleting. |
| `--force` | false | Skip the confirmation prompt. |

```bash
veriproc clean --before 2025-07-01T00:00:00Z --by processing-window --dry-run
```

Canonical runs are protected by the daemon's cleaning rules; see
[Operations §11.5](11-operations.md).

## 8.11 `health`, `readiness`, `version`

```bash
veriproc health         # GET /health
veriproc readiness      # GET /readiness
veriproc version [--check-api]   # local version; --check-api also queries the daemon
```

## 8.12 Output formats

- `table` (default) — human-readable columns.
- `json` — raw API JSON.
- `yaml` — YAML rendering of the response.

Use `json`/`yaml` for scripting; `table` for interactive use.

## 8.13 Exit codes

| Code | Name | Meaning |
|------|------|---------|
| 0 | OK | Success. |
| 1 | Generic | Unclassified error. |
| 2 | Usage | Bad arguments/flags. |
| 3 | Validation | Request failed validation. |
| 4 | NotFound | Resource does not exist. |
| 5 | Conflict | Idempotency/uniqueness conflict. |
| 6 | Auth | Authentication/authorization failure. |
| 7 | Unavailable | Service or dependency unavailable. |
| 8 | Indeterminate | Outcome could not be determined. |

These map onto the HTTP status codes returned by the daemon (see
[REST API](09-rest-api.md)).
