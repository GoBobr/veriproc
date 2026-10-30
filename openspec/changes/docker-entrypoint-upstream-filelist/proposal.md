# Proposal: Docker entrypoint override + upstream file lists in job orders

## Why

Two gaps found during the first real VeriProc deployment (ADPP TCE instance, adpp-tce):

1. **Docker entrypoint.** The slurm-docker executor runs
   `docker run <image> <executable> <args>` with no way to override the image
   ENTRYPOINT. Station images whose entrypoint is the processor binary itself
   (e.g. `co2m-ipf_adpp-l2:3.0rc5`, `ENTRYPOINT ["adpp-l2-ipf"]`) can never run
   a shell wrapper script: the entrypoint consumes the executable path as its
   own argument and fails (observed: the IPF tried to parse `run.sh` as a
   joborder).

2. **File list handoff.** There is no supported way for one station to decide
   the input file list of a downstream station's job order. `task-out.yaml`
   carries only `{station_id, window, split_group_id}`, and window matching
   cannot select files whose names the naming grammar cannot time-parse
   (VIIRS/NSIDC `VNP10A1.A2026298.h32v11....h5`). The current ADPP instance
   works around this with an out-of-band "joborder spool" directory — invisible
   to the daemon (no staging, no manifest, no fingerprint, no reprocessing
   story).

## What Changes

**Part A — executor `entrypoint` override (functional):**

- Station `execution.container` gains an optional `entrypoint` field (station
  schema + loader validation).
- The slurm-docker script generator emits `--entrypoint <value>` when set,
  before the image ref.
- Applies to `docker run` on the compute node; local executor unaffected.

**Part B — upstream file lists into job orders:**

- `task-out.yaml` downstream entries gain a typed `inputs:` payload
  (`[{file_type, files: [paths]}]`), validated and persisted with the created
  task.
- Station input declarations gain `source: upstream` (default remains rolling /
  window matching). For such inputs, resolution takes the file list from the
  creating task instead of scanning category folders, and stages (symlinks) the
  files into `<working_root>/input/` and the manifest exactly like
  window-matched inputs.
- The default joborder renderer then emits `inputs: [{file_type, files:
  [./input/...]}]` unchanged — injection falls out of the existing
  `jobOrderDocument` path; no renderer changes.

Both parts are independent and can be implemented, tested, and reviewed
separately (task groups A and B share no code).
