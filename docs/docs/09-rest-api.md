# 9. REST API

`veriprocd` exposes a versioned HTTP API under `/api/v1`. The `veriproc` CLI and the
operator console are both clients of this API. This page is the endpoint reference.

## 9.1 Conventions

- **Base path:** `/api/v1`. Health/readiness are also served at the root (`/health`,
  `/readiness`) for platform probes.
- **Content type:** JSON request and response bodies.
- **Authentication:** optional bearer token (`Authorization: Bearer <token>`); see §9.9.
- **Correlation:** send `X-Correlation-ID` to correlate a request across logs; the daemon
  generates one if absent.

## 9.2 Endpoint map

### Health
| Method | Path | Purpose |
|--------|------|---------|
| GET | `/health` and `/api/v1/health` | Liveness. |
| GET | `/readiness` and `/api/v1/readiness` | Readiness (dependencies up). |

### Tasks
| Method | Path | Purpose |
|--------|------|---------|
| POST | `/api/v1/tasks` | Submit a task. |
| GET | `/api/v1/tasks` | List/search tasks. |
| GET | `/api/v1/tasks/{task_id}` | Get a task. |
| GET | `/api/v1/tasks/{task_id}/runs` | List runs of a task. |
| POST | `/api/v1/tasks/{task_id}/retry` | Create a new retry run. |
| DELETE | `/api/v1/tasks/{task_id}` | Delete a task and its runs. |

### Runs, jobs, artifacts
| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/v1/runs` | List/search runs. |
| GET | `/api/v1/runs/{run_id}` | Get a run. |
| POST | `/api/v1/runs/{run_id}/cancel` | Request cancellation. |
| POST | `/api/v1/runs/{run_id}/promote` | Promote a duplicate to canonical. |
| GET | `/api/v1/runs/{run_id}/jobs` | List a run's jobs. |
| GET | `/api/v1/runs/{run_id}/artifacts` | List a run's artifacts. |
| GET | `/api/v1/runs/{run_id}/logs` | List a run's log artifacts. |
| DELETE | `/api/v1/runs/{run_id}` | Delete a run. |
| GET | `/api/v1/jobs/{job_id}` | Get a job. |
| GET | `/api/v1/artifacts/{artifact_id}/content` | Fetch artifact content. |

### Stations
| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/v1/stations` | List stations. |
| GET | `/api/v1/stations/summary` | Health summary (all stations). |
| GET | `/api/v1/stations/{station_id}/summary` | Health summary (one station). |
| POST | `/api/v1/stations/{station_id}/pause` | Pause admission. |
| POST | `/api/v1/stations/{station_id}/unpause` | Resume admission. |

### Split groups
| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/v1/groups` | List split groups. |
| GET | `/api/v1/groups/{group_id}` | Get a group. |
| POST | `/api/v1/groups/{group_id}/close` | Close a group (finalize fan-in). |

### Maintenance
| Method | Path | Purpose |
|--------|------|---------|
| POST | `/api/v1/maintenance/clean` | Bulk delete by filter. |

> Endpoint groups are only mounted if the corresponding service is configured, so a
> minimal deployment may expose a subset.

## 9.3 Submit a task

```http
POST /api/v1/tasks
Content-Type: application/json
Idempotency-Key: optional-client-key

{
  "destination": { "station_id": "scen-clim" },
  "window": { "start": "2025-07-03T11:00:00Z", "end": "2025-07-03T11:15:00Z" },
  "force": false,
  "priority": "normal",
  "split_group_id": null,
  "parent": { "task_id": null, "retry_index": null },
  "client_metadata": {}
}
```

Response `201 Created` (or `200 OK` on an idempotent re-submit):

```json
{
  "task_id": "scen-clim-20250703T110000000-a7sf7f",
  "station_id": "scen-clim",
  "start": "2025-07-03T11:00:00Z",
  "end": "2025-07-03T11:15:00Z",
  "state": "accepted",
  "force": false,
  "created_at": "2025-07-03T11:50:00Z",
  "latest_retry_index": 0,
  "latest_run_ref": "scen-clim-20250703T110000000-a7sf7f/r0"
}
```

`curl`:

```bash
curl -X POST http://localhost:8080/api/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{"destination":{"station_id":"scen-clim"},
       "window":{"start":"2025-07-03T11:00:00Z","end":"2025-07-03T11:15:00Z"}}'
```

## 9.4 List/query

List endpoints accept query parameters for filtering and `limit`/`cursor` for pagination.

- **Tasks:** `station_id`, `state`, `split_group_id`, `parent_task_id`, `limit`, `cursor`.
- **Runs:** `task_id`, `state`, `canonicality`, `station_id`, `limit`, `cursor`.
- **Groups:** `state`, `limit`.

```bash
curl 'http://localhost:8080/api/v1/runs?station_id=scen-clim&canonicality=canonical&limit=50'
```

## 9.5 Cancel & promote

```http
POST /api/v1/runs/{run_id}/cancel
{ "reason": "operator requested" }

POST /api/v1/runs/{run_id}/promote
{ "reason": "supersede original" }
```

`promote` requires a reason and turns a `duplicate` run canonical (routing it downstream).

## 9.6 Retry

```http
POST /api/v1/tasks/{task_id}/retry
```

Creates a new run (`r<N+1>`) for the task. See [Operations §11.3](11-operations.md).

## 9.7 Maintenance clean

```http
POST /api/v1/maintenance/clean
{
  "before": "2025-07-01T00:00:00Z",
  "by": "processing-window",
  "dry_run": true
}
```

Returns a report of what was (or would be) deleted.

## 9.8 Error envelope

All error responses use a normalized envelope:

```json
{ "error": { "code": "not_found", "message": "resource not found" } }
```

| HTTP | Typical cause |
|------|---------------|
| 400 Bad Request | Malformed/invalid request. |
| 401 / 403 | Missing/invalid token; insufficient role. |
| 404 Not Found | Unknown resource or route. |
| 405 Method Not Allowed | Known path, wrong method. |
| 409 Conflict | Idempotency/uniqueness conflict. |
| 422 Unprocessable Entity | Domain-logic rejection. |
| 429 Too Many Requests | Quota exceeded. |
| 503 Service Unavailable | Dependency down / not ready. |
| 504 Gateway Timeout | Slow upstream. |

These map onto the CLI exit codes in [CLI §8.13](08-cli.md).

## 9.9 Authentication & authorization

When authentication is configured, send a bearer token:

```bash
curl -H "Authorization: Bearer <token>" http://localhost:8080/api/v1/tasks
```

- **Roles:** `viewer` (read-only) and `operator` (read + write).
- **Mutating methods** (`POST`/`PUT`/`PATCH`/`DELETE`) require the `operator` role.
- **Quotas:** optional per-principal request rate limits are enforced on mutating
  requests; exceeding them returns `429`.

When authentication is not configured, the API is open (suitable only for trusted local
deployments).

## 9.10 Middleware chain

Every request passes through correlation-ID injection, request logging, panic recovery,
authentication, and quota enforcement before reaching a handler — so logs, auth, and rate
limits apply uniformly across all endpoints.
