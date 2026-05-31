# 11. Operations

This page covers day-to-day operation of a running instance: rolling archives and input
selection, publication, retries, split groups, cleaning, and recovery.

## 11.1 Rolling archives & input selection

Rolling archives are named read-write directories declared in
[`rolling_archives`](05-configuration.md) and grouped into ordered
[`product_categories`](05-configuration.md). When a run is prepared, each station input is
resolved by:

1. taking the input's `category` and scanning that category's folders **in order**;
2. parsing candidate filenames with the classical `naming.filenames.filename_pattern`;
3. selecting objects by the input's `window_match` rule against the (margin-adjusted)
   processing window.

Earlier folders win, so place authoritative archives ahead of fallbacks. The selected set
is frozen into the run's **manifest** and symlinked under the working root's `input/`.

**Tip:** if a mandatory input does not resolve, the run fails early in `preparing`. Check
that the file type, category folders, and filename pattern actually match the files on
disk.

## 11.2 Output validation & publication

After a job finishes, finalize validates the declared `outputs[]` in the working root. An
output marked with `publish` is queued for **publication** to its target rolling archive.

The **publisher** loop:

- runs periodically (batch-based), independent of the dispatcher;
- copies/moves/links each pending output into the target archive using the station's
  `publish.mode` (`copy`/`move`/`link`);
- records a publication entry with the target path and method.

Publication is **asynchronous**: a run can be `complete` before all its outputs are
published. Failed publications are marked and retried/inspected separately.

## 11.3 Retries

A retry creates a new run for an existing task with the next retry index (`r1`, `r2`, …):

```bash
curl -X POST http://localhost:8080/api/v1/tasks/{task_id}/retry
```

The new run re-resolves inputs and re-renders the job order. Because the **processing
fingerprint** is computed from the output-affecting context, a retry that resolves the same
inputs and revision is a `duplicate`; a retry after inputs/revision changed is a new
`canonical` candidate. This is what makes retries safe.

## 11.4 Split groups (fan-out / fan-in)

Use split groups when a wide window is divided into sub-windows processed independently and
then aggregated.

- Associate fan-out tasks with a group via `--split-group <id>` on `submit` (or the API
  `split_group_id`).
- The group state is **derived** from members: `open`/`aggregating` while any member is
  active; `complete` when all members are terminal and at least one is canonical; `failed`
  when a member fails and none is canonical.
- Inspect and finalize:

```bash
veriproc group list
veriproc group get <split_group_id>
veriproc group close <split_group_id>   # finalize fan-in
```

Members carry a role: `contributing` (fan-out) or `aggregation` (fan-in). Closing a group
coordinates completion so aggregation can proceed deterministically.

## 11.5 Cleaning & deletion

Three levels of deletion are available:

| Scope | CLI | API |
|-------|-----|-----|
| One run | `veriproc run delete <run_ref>` | `DELETE /api/v1/runs/{run_id}` |
| One task (all runs) | `veriproc task delete <task_id>` | `DELETE /api/v1/tasks/{task_id}` |
| Bulk by cutoff | `veriproc clean --before … --by …` | `POST /api/v1/maintenance/clean` |

Behavior:

- **Dry-run first.** `--dry-run` returns a report (`TasksDeleted`, `RunsDeleted`,
  `ArtifactsDeleted`, `WorkingRootsRemoved`, `Errors`) without deleting.
- Deletion removes database rows **and** the associated working roots on disk.
- Canonical runs are protected by the cleaning rules; bulk clean targets non-canonical and
  terminal state by default. Prefer cleaning by `processing-window` to retire old sensing
  windows, or `processing-time` to retire by when work was created.

```bash
veriproc clean --before 2025-07-01T00:00:00Z --by processing-window --dry-run
veriproc clean --before 2025-07-01T00:00:00Z --by processing-window --force
```

## 11.6 Recovery & reconciliation

VeriProc is designed to survive daemon restarts without duplicating canonical work:

- Control state is durable in SQLite; the run state machine uses conditional transitions so
  a step is never skipped or repeated.
- On restart, in-flight runs are reconciled against executor state and filesystem markers.

The **reconciler** loop handles **stale runs** — runs in `running` with no poll progress
past a threshold. It reads the exit-code marker written by the executor/wrapper, and if the
job has actually finished, moves the run into `finalizing` so it can complete. This recovers
from lost scheduler tracking (e.g. an executor restart) without manual intervention.

## 11.7 Station pause/unpause

Pausing a station stops admission of new work for it without affecting in-flight runs:

```bash
veriproc station pause <station_id>
veriproc station unpause <station_id>
```

Use this to drain or hold a station during maintenance.

## 11.8 Health & readiness

- `GET /health` → `{"status": "up" | "shutting_down"}` — process liveness.
- `GET /readiness` → `{"status": "ready" | "not_ready", "dependencies": [...]}` — all
  registered dependency checks pass.

Wire `/health` to a liveness probe and `/readiness` to a readiness probe. On shutdown the
daemon flips readiness to `not_ready` first, so load balancers drain it before it exits.

## 11.9 Observability

- Structured logs (zerolog) in `json` (machine ingest) or `console` (human) format.
- Each request carries a correlation ID (`X-Correlation-ID`) threaded through logs.
- Run/job records, manifests, artifacts, and provenance links provide an audit trail; the
  working root holds the durable job order, inputs, outputs, and logs for any run.
