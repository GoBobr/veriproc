# Design: Docker entrypoint override + upstream file lists

## Context

- Executor script generation: `internal/executor/slurm.go` builds the sbatch
  shell script; docker runs emit `docker run --rm --name ... -v ... -u ... -e ... <image> <executable> <args>`.
- Task-out channel: a station's `task-out.yaml` (`internal/runs/finalize.go`,
  `readTaskOutDescriptor` → `buildTaskOutDownstreamTasks`) creates downstream
  tasks keyed by `{station_id, window, split_group_id, key}`; no payload today.
- Input resolution: `internal/runs/service.go::resolveManifest` +
  `classicalSelectCandidates` select files from category/rolling folders via
  the naming grammar and window matching; winners are symlinked into
  `<working_root>/input/` and recorded in the manifest. The default joborder
  renderer (`jobOrderDocument`) emits `inputs: [{file_type, files: [symlinks]}]`
  from the manifest.

## Goals / Non-Goals

Goals:
- A station must be able to run a wrapper script in an image whose ENTRYPOINT
  is a processor binary (Part A).
- A station must be able to hand a concrete input file list to a downstream
  station's job order, with daemon-side staging, manifest, and fingerprinting
  (Part B).

Non-goals:
- Generic free-form payloads on tasks (rejected: breaks the
  "joborder = f(revision, window, manifest)" determinism model).
- New date formats in the naming grammar (does not solve "upstream decides the
  package" cases).
- Any change to the public task-submission API.

## Decisions

### D1: `entrypoint` as a station container field (Part A)

Station `execution.container.entrypoint` (optional **string**, docker
semantics: passed to `docker run --entrypoint` as a single token — the
entrypoint binary — matching the `--entrypoint` CLI contract; everything
after the image, i.e. the station `executable` + args, becomes the command).
Multi-token composites (e.g. `sh -c`) compose across the existing fields
(`entrypoint: /bin/sh`, executable+args as the rest), exactly like raw
docker. A list form is rejected: it would duplicate the compose-across-fields
semantics and complicate shell-quoting in the generated sbatch script.
Station-level (not instance-level) because it is a property of the station
image contract; instances pin images via `image: <env:...>` as today.
Subsumes the "executable-is-the-joborder" use case
(`entrypoint: adpp-l2-ipf` + `executable: <joborder path>` requires no new
mode).

Alternatives considered: instance-level flag (wrong scope); wrapper images
(operational burden, one per image); list form (rejected, see above).

### D2: typed `inputs` payload on task-out, `source: upstream` on inputs (Part B)

The payload is a typed file list (`[{file_type, files}]`), not a generic map:
the daemon can validate (files exist, file_type declared by the target
station) and stage it through the existing manifest path. `file_type` must be
a declared input of the downstream station — the upstream names *which*
declared input it feeds, never arbitrary joborder content.

Resolution: inputs with `source: upstream` skip folder scanning entirely and
take their file list from the creating task's stored payload; staging
(symlinks into `input/`), manifest records, integrity (sha256 available_only),
and the joborder renderer are reused unchanged, so processing fingerprints and
reprocessing semantics stay intact. An `upstream` input without a payload on
the task is a run-preparation failure with a clear `failure_reason`.

Persistence: stored on the task row (nullable JSON column via the store
migration path); the submit API is NOT extended — payload arrives only via
task-out (station-to-station trust boundary unchanged).

## Risks / Trade-offs

- Large payloads (thousands of files) stored on the task row: acceptable at
  JSON-in-SQLite scale for this system (joborders already carry such lists);
  Deduplication key grows by the file-set hash.
- Windows-style absolute paths / cross-mount expectations: staging symlinks
  has the same constraints as today's window-matched staging.

## Migration Plan

Backwards compatible: `entrypoint` and `source` default to current behavior;
task-out without `inputs` and inputs without `source` behave exactly as today.
No config migration needed. The adpp-tce spool workaround is retired
out-of-repo once Part B lands (task B8).

## Open Questions

- None. (Resolved: `entrypoint` is a plain string, single docker token — see
  D1.)
