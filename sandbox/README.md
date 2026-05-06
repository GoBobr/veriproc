# VeriProc Sandbox

A minimal live sandbox for exercising the **Core MVP — stub-executor profile**
(M0–M7): server startup, station loading, task submission, run inspection,
artifact/log metadata, split-group aggregation, publication status, and the
background reconciler.

The sandbox is live and useful for API/CLI workflows under the stub profile.
The shell scripts in `stations/*/scripts/` are forward-compatible fixtures:
they describe the contract a future SLURM/Kubernetes executor would honor,
but the current `veriprocd` profile still executes work through the built-in
`StubExecutor`.

---

## Layout

```
sandbox/
├── README.md                  # this file
├── stations/
│   ├── station-a/             # STATION-A / SCE_2
│   ├── station-b/             # STATION-B / TRACK_L1
│   └── station-c/             # STATION-C / AGG_DAILY
└── data/                      # created at runtime (sqlite + archive)
```

Each `station.yaml` contains only human-maintained fields such as station id,
processing type, description, scripts, outputs, and metadata. VeriProc
computes the immutable station revision `content_hash` at load time from a
deterministic canonical representation of the file. You should not type or
maintain hashes for normal sandbox use.

---

## 1. Build

From the repo root:

```bash
go build -o ./bin/veriprocd ./cmd/veriprocd
go build -o ./bin/veriproc  ./cmd/veriproc
```

## 2. Start the server with station auto-loading

```bash
mkdir -p sandbox/data
export VERIPROC_DSN="sqlite://$PWD/sandbox/data/veriproc.db"
export VERIPROC_ARCHIVE_BASE="$PWD/sandbox/data/archive"
export VERIPROC_WORKING_ROOT_BASE="$PWD/sandbox/data/working-roots"
export VERIPROC_AUTH_TOKENS="alice:operator:dev-token-alice:120;bob:reader:dev-token-bob:60"
export VERIPROC_STATION_DIR="$PWD/sandbox/stations"

./bin/veriprocd
```

`VERIPROC_STATION_DIR` scans immediate child directories for
`station.yaml` files, e.g. `sandbox/stations/station-a/station.yaml`.
Duplicate `station_id` values are rejected unless they are the exact same
revision already registered through another mechanism. Content changes produce
a different computed hash; in this M0–M7 profile, only one revision per station
id may be loaded at startup.

The server listens on `127.0.0.1:8080` by default. Liveness: `GET /health`.
Readiness: `GET /api/v1/health`.

### Optional fallback seeding

`VERIPROC_SEED_STATIONS` remains supported for tests and tiny deployments,
but it is not the recommended sandbox path. It accepts either short form:

```bash
export VERIPROC_SEED_STATIONS="STATION-A:SCE_2;STATION-B:TRACK_L1"
```

or the legacy explicit form:

```bash
export VERIPROC_SEED_STATIONS="STATION-A:SCE_2:sha256:<hex>:veriproc.station/v1"
```

When both `VERIPROC_STATION_DIR` and `VERIPROC_SEED_STATIONS` are set, file
loaded stations are registered first, then seed entries are added. Conflicting
duplicates fail startup so station identity remains deterministic.

## 3. Configure the CLI

```bash
export VERIPROC_API_URL="http://localhost:8080"
export VERIPROC_TOKEN="dev-token-alice"
export VERIPROC_OUTPUT="table"   # or json / yaml
```

## 4. Walk through the M7 surface

### 4a. Submit a single task

```bash
./bin/veriproc submit \
  --station STATION-A \
  --start 2025-07-03T11:00:00Z \
  --end   2025-07-03T11:15:00Z \
  --idempotency-key demo-001
```

Inspect it:

```bash
./bin/veriproc task list
./bin/veriproc task get <task-id>
```

### 4b. Watch the run materialize

```bash
./bin/veriproc run list --task <task-id>
./bin/veriproc run get <run-id>
./bin/veriproc run jobs <run-id>
./bin/veriproc artifact list --run <run-id>
./bin/veriproc logs <run-id>
```

`artifact list` will show two artifacts for every successful stub run:

| logical_type | file | notes |
|---|---|---|
| `joborder` | `<working-root>/job-order.yaml` | input spec written before executor submission |
| `log` | `<working-root>/logs/run.log` | stub lifecycle log written at finalization |

The station `outputs:` entries (e.g. `result.json`) are **not** written by the
stub executor — a real SLURM/Kubernetes executor would produce those files.


### 4c. Split-group aggregation

Submit two windows tagged with the same `--split-group`, then close the group
to materialize its terminal state:

```bash
./bin/veriproc submit --station STATION-C \
  --start 2025-07-03T00:00:00Z --end 2025-07-03T12:00:00Z \
  --split-group day-2025-07-03

./bin/veriproc submit --station STATION-C \
  --start 2025-07-03T12:00:00Z --end 2025-07-04T00:00:00Z \
  --split-group day-2025-07-03

./bin/veriproc group list
./bin/veriproc group get day-2025-07-03
./bin/veriproc group close day-2025-07-03
```

### 4d. Cancel safely

```bash
./bin/veriproc cancel --yes --reason "operator stop" <run-id>
```

If the background reconciler is currently repairing that run, the API returns
HTTP 409 `reconciliation_in_progress` and the CLI exits 5, matching Spec
§6.10 conflict behavior.

### 4e. Promote a duplicate

```bash
./bin/veriproc promote --reason "manual override" --actor alice <run-id>
```

## 5. Reset

```bash
rm -rf sandbox/data
```

## 6. Reference

- CLI surface: `specs/6.command_line_interface.md`
- Conformance report: `docs/milestones/M0-M7-report.md`
- Spec exit codes: §6.10
