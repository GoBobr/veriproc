# Domain Model Specification (delta): Upstream-declared input file lists

## ADDED Requirements

### Requirement: task-out inputs payload

A station's `task-out.yaml` downstream entry MAY declare an `inputs` payload:
a list of `{file_type, files}` entries with absolute file paths. The daemon
MUST validate at task-out processing time that every listed file exists and
that each `file_type` is declared as an input of the target station, and MUST
persist the payload with the created task. Downstream entries without
`inputs` behave exactly as today.

#### Scenario: Dial-a-filelist handoff
- **GIVEN** a `task-out.yaml` downstream entry for station S with
  `inputs: [{file_type: T, files: [p1, p2]}]`
- **WHEN** the task for S is created and prepared
- **THEN** the run's input manifest for T contains p1 and p2 and the
  joborder's `inputs` section lists the staged paths

#### Scenario: Invalid payload rejected
- **GIVEN** a task-out `inputs` entry whose file_type is not declared by the
  target station, or lists a non-existent file
- **WHEN** the task-out is processed
- **THEN** task creation fails with a descriptive error and no run is started

### Requirement: input source `upstream`

A station input declaration MAY set `source: upstream`, meaning its file list
is supplied by the creating task's declared inputs payload rather than by
window matching against category folders. Run preparation MUST stage the
payload files into the working root and record them in the manifest as for
window-matched inputs; a missing payload MUST fail preparation.

#### Scenario: Upstream input resolves from payload
- **GIVEN** station S declares an input with `source: upstream` and its task
  carries a declared inputs payload for that file_type
- **WHEN** the run is prepared
- **THEN** the listed files are staged and appear in the manifest and joborder
  without any folder scan or window matching

#### Scenario: Missing payload
- **GIVEN** station S declares an input with `source: upstream` and its task
  carries no inputs payload for that file_type
- **WHEN** the run is prepared
- **THEN** preparation fails with a descriptive failure reason and no job is
  dispatched
