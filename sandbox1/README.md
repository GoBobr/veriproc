# VeriProc Sandbox

This sandbox demonstrates the local developer profile of VeriProc with a real execution path. The daemon loads a top-level instance configuration, resolves inputs from configured rolling archives, writes a spec-shaped `joborder.yaml`, launches station scripts through the local executor, validates real output files, publishes selected outputs back to archives, and creates downstream tasks including descriptor-driven fan-out/fan-in.

The profile is still lightweight: it uses SQLite, local POSIX paths, and local process execution rather than SLURM. The fan-out/fan-in scenario (stations C -> D x 10 -> E) demonstrates split-group tracking and classical input matching across multiple rolling archives.

## Layout

```text
sandbox/
├── instance.yaml                  # top-level VeriProc instance config
├── rolling-archives/
│   ├── aux/                       # auxiliary input fixtures
│   └── prods/                     # product fixtures and A/B outputs
├── data/temp-rolling-archives/
│   ├── micro/                     # MICROBIG and PROCBIG fan-out products
│   └── proc/                      # FINAL aggregation products
└── stations/
    ├── station-a/   statA  resolves two inputs, publishes SCE_2__ICM______, routes to B
    ├── station-b/   statB  consumes A's output, publishes SCE_2__BIG______, routes to C
    ├── station-c/   statC  fan-out splitter: produces 10 SCE_2__MICROBIG_ granules,
    │                        publishes to micro-ra and declares 10 statD tasks in task-out.yaml
    ├── station-d/   statD  parallel worker: processes one granule -> SCE_2__PROCBIG__
    └── station-e/   statE  fan-in aggregator: collects available PROCBIG -> SCE_2__FINAL____
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

### Linear chain A -> B -> C

Submit STATION-A. The daemon automatically routes A -> B -> C on completion.

```bash
./bin/veriproc submit \
  --station statA \
  --start 2025-07-03T11:00:00Z \
  --end   2025-07-03T11:15:00Z \
  --idempotency-key sandbox-a-001
```

Station-C's script fans out through `task-out.yaml`: it splits the 15-minute window into ten 90-second slices, writes 10 `SCE_2__MICROBIG_` granules, and writes a descriptor declaring 10 `statD` tasks all sharing one split group. During finalization, the backend validates all matching MICROBIG outputs as separate artifacts, publishes them to the `micro-ra` archive, validates the descriptor, records it as a `task_out` artifact, closes the bounded group, and creates the `statD` tasks.

### Monitor the split group

```bash
# List all groups
./bin/veriproc group list

# Get the specific group created by station C (id = sg-<task_id_of_statC>)
./bin/veriproc group get sg-<statC_task_id>

# Watch task states for all statD members
./bin/veriproc task list --station statD
```

The group transitions through `open` -> `aggregating` (once all 10 tasks have been submitted and the group is closed by the system) -> `complete` once all expected members are terminal and at least one canonical statD result exists. If every member fails to produce a canonical result, the group transitions to `failed`.

### Fan-in: station E auto-trigger

Once all 10 station-D members have reached a terminal state, the backend evaluates the closed split group. When readiness finds at least one canonical member, it submits the descriptor-declared aggregation station (`statE`) idempotently; failed members are excluded under the sandbox's partial-aggregation policy.

Station-E uses `window_match: overlaps` with the full 15-minute window, so the classical matcher selects available `SCE_2__PROCBIG__` granules from the `micro-ra` archive (one winner per 90-second interval group) and writes the aggregated `SCE_2__FINAL____` product to `proc-ra`.

### Inspect state

```bash
./bin/veriproc task list
./bin/veriproc run list --task <task-id>
./bin/veriproc run get <run-id>
./bin/veriproc run jobs <run-id>
./bin/veriproc artifact list --run <run-id>
./bin/veriproc logs <run-id>
```

### Check the filesystem record

```bash
find sandbox/data/working-roots -maxdepth 5 -type f -o -type l | sort
find sandbox/rolling-archives -maxdepth 2 -type f | sort
```

## Fixture Data

The `prods` archive contains deterministic text products using the structured filename contract: mission id, fixed-width 16-character file type, UTC start/end/generation timestamps, and a free suffix. Station-A resolves `CLI_1B_RAD______` and `CO2_1A_GEO______` inputs from `prods`, plus a `SCE_2__CAMF___AX` auxiliary directory from the `aux` archive. See `rolling-archives/README.md` for the purpose of each file.

## Filename Convention

All product files follow the instance-wide pattern:

```
<MISSION_ID>_<FILE_TYPE>_??_<START_TIME>_<END_TIME>_<GENERATION_TIME>_<suffix>.nc
```

| Component       | Length | Example              |
|-----------------|--------|----------------------|
| MISSION_ID      | 4      | `CDMA`               |
| FILE_TYPE       | 16     | `SCE_2__MICROBIG_`   |
| ??              | 2      | `ON`                 |
| START_TIME      | 15     | `20250703T110000`    |
| END_TIME        | 15     | `20250703T110130`    |
| GENERATION_TIME | 15     | `20250703T120000`    |

## Reset

```bash
rm -rf sandbox/data
mkdir -p sandbox/data/temp-rolling-archives/micro sandbox/data/temp-rolling-archives/proc
```

## Current Narrowing

- The local executor is for development and conformance-style tests; it is not a SLURM adapter.
- Rolling archive publication is implemented for local filesystem copy mode in this profile.
- Station-C uses algorithm-produced `task-out.yaml` for the `statD` fan-out; the backend owns downstream task creation from the descriptor.
- Station-E is submitted automatically by the backend once the descriptor-declared split group reaches readiness, including partial readiness when at least one statD member succeeded canonically and all expected members are terminal.
- Provenance links are persisted for downstream creation, but the public API is still narrower than the full provenance query model.
