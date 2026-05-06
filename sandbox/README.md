# VeriProc Sandbox

This sandbox demonstrates the local developer profile of VeriProc with a real execution path. The daemon loads a top-level instance configuration, resolves inputs from a configured rolling archive, writes a spec-shaped `joborder.yaml`, launches station scripts through the local executor, validates real output files, publishes selected outputs back to the archive, and creates one linear downstream task.

The profile is still lightweight: it uses SQLite, local POSIX paths, and local process execution rather than SLURM. Split-group aggregation and rich provenance queries remain intentionally narrow compared with the full specification.

## Layout

```text
sandbox/
├── instance.yaml                  # top-level VeriProc instance config
├── rolling-archives/
│   ├── README.md
│   └── hot/                       # configured rolling archive fixtures
├── stations/
│   ├── station-a/                 # resolves two inputs, publishes output, routes to B
│   ├── station-b/                 # consumes A's published output
│   └── station-c/                 # retained split-group fixture
└── data/                          # runtime SQLite DB and working roots
```

## Build

From the repository root:

```bash
go build -o ./bin/veriprocd ./cmd/veriprocd
go build -o ./bin/veriproc  ./cmd/veriproc
```

## Start The Daemon

```bash
mkdir -p sandbox/data
./bin/veriprocd --config sandbox/instance.yaml
```

The instance file selects the `local` executor. Station scripts are actually run as OS processes and receive the runtime variables required by the spec, including `VERIPROC_WORKING_ROOT`, `VERIPROC_STATION_ID`, `VERIPROC_RUN_ID`, `VERIPROC_TASK_ID`, and `VERIPROC_JOBORDER_PATH`.

In another terminal:

```bash
export VERIPROC_API_URL="http://localhost:8080"
export VERIPROC_OUTPUT="table"
```

Authentication is disabled by default in `sandbox/instance.yaml` for a small local loop. Add `VERIPROC_AUTH_TOKENS` if you want to exercise authenticated CLI calls.

## Demo Flow

Submit STATION-A:

```bash
./bin/veriproc submit \
  --station STATION-A \
  --start 2025-07-03T11:00:00Z \
  --end   2025-07-03T11:15:00Z \
  --idempotency-key sandbox-a-001
```

Inspect state:

```bash
./bin/veriproc task list
./bin/veriproc run list --task <task-id>
./bin/veriproc run get <run-id>
./bin/veriproc run jobs <run-id>
./bin/veriproc artifact list --run <run-id>
./bin/veriproc logs <run-id>
```

Check the filesystem record:

```bash
find sandbox/data/working-roots -maxdepth 5 -type f -o -type l | sort
cat sandbox/rolling-archives/hot/STATION-A/result-a.json
```

The STATION-A working root contains:

- `joborder.yaml`
- `input/` symlinks to selected archive products
- `output/result-a.json`
- `logs/run.log` captured from the station script
- `temp/`
- `manifest/resolved-inputs.yaml`

STATION-A's default downstream rule creates a STATION-B task only after A has validated outputs, recorded artifacts, elected canonicality, and published the selected output. STATION-B resolves `STATION-A/result-a.json` from the archive and writes `output/track.csv` in its own working root.

## Fixture Data

The `hot` archive contains six deterministic text products. There are two declared input file types for STATION-A (`PRIMARY_A` and `AUX_A`) plus extra non-selected files so folder scanning and deterministic selection are visible. See `rolling-archives/README.md` for the purpose of each file.

## Reset

```bash
rm -rf sandbox/data sandbox/rolling-archives/hot/STATION-A
```

## Current Narrowing

- The local executor is for development and conformance-style tests; it is not a SLURM adapter.
- Rolling archive publication is implemented for local filesystem copy mode in this profile.
- Dynamic `task-out.yaml` routing and full split-group aggregation semantics remain deferred; the sandbox demonstrates simple station-default downstream routing.
- Provenance links are persisted for downstream creation, but the public API is still narrower than the full provenance query model.
