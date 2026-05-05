# VeriProc Sandbox

A minimal sandbox for exercising the **Core MVP — stub-executor profile**
(M0–M7) end-to-end: server, CLI, three stations, split-group aggregation,
and the background reconciler.

> Note: artifacts under `stations/*/scripts/` are illustrative. The current
> profile uses the **stub executor** baked into `veriprocd`; the per-station
> `run.sh` scripts document the contract a future SLURM/Kubernetes adapter
> would honor.

---

## Layout

```
sandbox/
├── README.md                  # this file
├── stations/
│   ├── station-a/             # SCE_2 — small JSON window
│   ├── station-b/             # TRACK_L1 — multi-row CSV
│   └── station-c/             # AGG_DAILY — daily aggregator
└── data/                      # created at runtime (sqlite + archive)
```

---

## 1. Build

From the repo root:

```bash
go build -o ./bin/veriprocd ./cmd/veriprocd
go build -o ./bin/veriproc   ./cmd/veriproc
```

## 2. Start the server

```bash
mkdir -p sandbox/data
export VERIPROC_DSN="sqlite://$PWD/sandbox/data/veriproc.db"
export VERIPROC_ARCHIVE_BASE="$PWD/sandbox/data/archive"
export VERIPROC_AUTH_TOKENS="alice:operator:dev-token-alice:120;bob:reader:dev-token-bob:60"
export VERIPROC_SEED_STATIONS="\
STATION-A:SCE_2:sha256:0000000000000000000000000000000000000000000000000000000000000aaa:veriproc.station/v1;\
STATION-B:TRACK_L1:sha256:0000000000000000000000000000000000000000000000000000000000000bbb:veriproc.station/v1;\
STATION-C:AGG_DAILY:sha256:0000000000000000000000000000000000000000000000000000000000000ccc:veriproc.station/v1"

./bin/veriprocd
```

The server listens on `:8080` by default. Liveness: `GET /health`.
Readiness: `GET /api/v1/health`.

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
./bin/veriproc run get  <run-id>
./bin/veriproc artifact list --run <run-id>
./bin/veriproc logs <run-id>
```

### 4c. Cancel safely (Spec §6.10 → exit 5 on conflict)

```bash
./bin/veriproc cancel --yes --reason "operator stop" <run-id>
```

### 4d. Promote a duplicate (Spec §5.5.6)

```bash
./bin/veriproc promote --reason "manual override" --actor alice <run-id>
```

### 4e. Split-group aggregation (M7)

Submit two windows tagged with the same `--split-group`, then close the
group to materialize its terminal state:

```bash
./bin/veriproc submit --station STATION-C \
  --start 2025-07-03T00:00:00Z --end 2025-07-03T12:00:00Z \
  --split-group day-2025-07-03

./bin/veriproc submit --station STATION-C \
  --start 2025-07-03T12:00:00Z --end 2025-07-04T00:00:00Z \
  --split-group day-2025-07-03

./bin/veriproc group list
./bin/veriproc group get   day-2025-07-03
./bin/veriproc group close day-2025-07-03
```

### 4f. Reconciler

The background reconciler ticks every **15s** and stamps
`reconciliation_started_at` on any dispatched/running run whose job has not
been observed for **60s** (see `cmd/veriprocd/main.go`). While the marker is
set, `cancel` and `promote` return HTTP 409 `reconciliation_in_progress`
(CLI exit 5).

To watch it work, leave the server idle for ~75s after a `submit` and tail
the logs for `reconciler.tick`.

## 5. Reset

```bash
rm -rf sandbox/data
```

## 6. Reference

- CLI surface: `specs/6.command_line_interface.md`
- Conformance report: `docs/milestones/M0-M7-report.md`
- Spec exit codes: §6.10
