# Tasks: Docker entrypoint override + upstream file lists

Two independent parts; address them separately. Part A touches only the
executor; Part B touches task-out parsing, persistence, and run preparation.
No shared code.

## Part A — Docker entrypoint override

- [x] A1. Station schema: add optional `execution.container.entrypoint`
      (string, single docker token, non-empty) to
      `internal/stations/loader.go` (Container struct), with validation (no
      empty string; list form rejected; unknown-field rejection stays intact).
- [x] A2. Executor: in `internal/executor/slurm.go` (docker run builder),
      emit `--entrypoint <shellQuote(value)> \` between the env flags and the
      image ref when `effective.Container.Entrypoint != ""`.
- [x] A3. Unit tests: extend `internal/executor/slurm_test.go` — generated
      script contains `--entrypoint` + value when set, and no `--entrypoint`
      when unset (existing cases must stay green).
      (`TestSlurmExecutorDockerWrapperEntrypoint`, plus
      `TestLoader_ContainerEntrypoint` variants in loader_test.go.)
- [x] A4. Docs: document `execution.container.entrypoint` in
      `docs/docs/06-stations.md` (execution section), including the motivating
      case: images whose ENTRYPOINT is the processor binary.
      (Also documented in `docs/docs/07-executors.md` §Overriding the image
      ENTRYPOINT.)
- [x] A5. E2E validation (manual or e2e fixture): run a station with
      `mode: slurm-docker`, image whose entrypoint is a binary, and
      `entrypoint: ["/bin/sh", "-c"]` (or string form) with the existing
      `executable` script — the script must actually execute.
      Done 2026-10-30 (live, adpp-tce srfcam on co2m009): ADPP image
      `co2m-ipf_adpp-l2:3.0rc5` (ENTRYPOINT adpp-l2-ipf) with
      `entrypoint: /bin/bash`; generated slurm-wrapper emitted
      `--entrypoint '/bin/bash'`, run.sh executed inside the container
      (container hostname in run_out.log), slurm job 6834 ran to success,
      task srfcam-20261025T000000000-8336f0 completed.

## Part B — upstream file lists in job orders

- [x] B1. task-out schema: extend `taskOutDownstreamYAML`
      (`internal/runs/finalize.go`) with `inputs []taskOutInputYAML
      {file_type, files []string}`; parse + strict validation in
      `readTaskOutDescriptor` / `buildTaskOutDownstreamTasks`:
      - every listed file must exist at task-out read time,
      - `file_type` must be declared by the target station's `inputs:`,
      - dedupe key extended to include the file set.
- [x] B2. Persistence: store the declared inputs with the created task
      (nullable JSON column on `tasks` + store migration, or equivalent in
      `internal/store`); expose via task read APIs.
      (`tasks.declared_inputs`, migration `0004_task_declared_inputs.sql`,
      `TaskRecord.DeclaredInputs`.)
- [x] B3. Loader: add `source` field to input declarations
      (`internal/stations/loader.go`), values `rolling` (default) and
      `upstream`; validate `upstream` inputs are `mandatory` and have no
      `window_match`/`pattern`/`rolling_folders` overrides (they are
      meaningless there).
- [x] B4. Resolution: in `internal/runs/service.go::resolveManifest`, when an
      input has `source: upstream`, take the file list from the task's stored
      declaration instead of `classicalSelectCandidates`, and symlink-stage
      each file into `<working_root>/input/<basename>` + write manifest
      records (reuse the existing staging code path).
- [x] B5. Joborder: verify the default renderer emits
      `inputs: [{file_type, files: [./input/...]}]` from the manifest with no
      renderer changes; add a regression test in the joborder rendering tests.
      (Asserted in `TestRuns_TaskOutInputsPayload_StageManifestJobOrder`;
      also confirmed live in the adpp-tce day-298 run.)
- [x] B6. Unit + integration tests: task-out with inputs → task created with
      payload → run prepared → manifest lists staged symlinks → joborder
      contains the file list; negative tests (missing file, undeclared
      file_type, upstream input without payload → run fails with clear
      failure_reason).
      (`internal/runs/taskout_inputs_test.go`: payload/negative/no-payload
      tests; loader tests incl. `TestLoader_UpstreamInputCategoryOptional`.)
- [x] B7. Docs: document `source: upstream` inputs and the `task-out.yaml`
      `inputs:` payload in `docs/docs/06-stations.md` and the task-out /
      joborder sections of `docs/docs/`.
- [x] B8. Migration of the adpp-tce instance (reference validation): replace
      the wchinp joborder spool with task-out `inputs:` payload; srfich
      declares `source: upstream`; run the day-298 pipeline end-to-end
      (out-of-repo follow-up, no veriproc code).
      Done 2026-10-30: wchinp cooked 2026298 (331 files) with `inputs:`
      payload → srfich task srfich-20261025T000000000-abeaad auto-created,
      331 symlinks staged into `input/`, joborder.SRFICH.yml rendered with
      the full file list, run `succeeded`, dummy output published to
      rolling.adpp.

## Verification

- Full `go test ./...` green; new tests from A3 and B6 included.
- `openspec validate` passes for this change before archive.
