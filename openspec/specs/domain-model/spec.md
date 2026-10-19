# Domain Model Specification

## Purpose

This specification defines the conceptual entities and terminology of the VeriProc domain: stations, tasks, runs, job orders, manifests, split groups, artifacts, fingerprints, and their supporting configuration and execution concepts. Each entity is specified normatively for the Go implementation in `internal/runs`, `internal/store`, and related packages. Unless otherwise stated, every persisted entity has exactly one canonical identity model; runs are the exception, using a task-scoped composite identity.

## Requirements

### Requirement: Identity Model Foundation

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Reject bare retry index as run reference
- **GIVEN** a task with at least one run **WHEN** a parent-run reference is created pointing at that run **THEN** the reference identifies the parent task and the run's task-scoped `retry_index`, not a bare retry index or internal run key.

**Additional normative rules for this requirement:**

- **GIVEN** a stored run record **WHEN** it is referenced conceptually **THEN** the reference is the pair of its parent `task_id` and its `retry_index`, and `retry_index` alone is never treated as an identifier.
Every persisted entity SHALL have exactly one canonical identity model. Runs MUST be identified canonically by the composite `(task_id, retry_index)`; all other persisted entities MUST use a single globally unique canonical identifier. Implementations MUST NOT derive task identifiers by concatenating parent run references, downstream ordinals, station names, provenance hints, or similar context; provenance, downstream derivation, split-group membership, parent-child links, and retry history MUST be represented by explicit fields and relationships.

- **GIVEN** downstream task creation **WHEN** a child task identifier is generated **THEN** it encodes no cross-entity lineage, lifecycle state, scheduler identity, working-root path, or provenance hint from its parent.
### Requirement: Station Entity

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Invalid station revision rejected for run generation
- **GIVEN** a station revision that fails configuration validation **WHEN** run generation is attempted **THEN** the revision is not used for run generation.

#### Scenario: Multi-object output declaration
- **GIVEN** a station output declaration that can yield more than one filesystem object for the same `file_type` **WHEN** the station is validated **THEN** it must declare multi-object validation explicitly, and each matching object becomes a distinct output artifact for validation, publication, and provenance.

**Additional normative rules for this requirement:**

- **GIVEN** a station configuration being loaded **WHEN** it is validated **THEN** it must validate against the station configuration schema, reference a valid execution mode, reference only declared category names, and reference only configured rolling archive identifiers for `rolling:<id>` folder references.
A station is the declarative specification of one processing step, carried as an immutable persisted station revision. A station MUST carry `station_id`, `station_name`, execution mode, execution image or executable reference, structured execution executable and argument list, resource requirements, input definitions, and output definitions; it MAY carry optional category-folder overrides (`rolling_folders`), per-input/per-output `filename_pattern` overrides, per-input `window_match` and `filter` policies, default downstream list, split-group and dynamic split-declaration policies, output publication policy, runtime parameters, `joborder` renderer settings, validation hooks, timeout policy, and cancellation policy.

- **GIVEN** a station input or output declaring `filename_pattern` **WHEN** the pattern is validated **THEN** it must use the supported placeholder vocabulary of the instance naming policy, and any `<FILE_TYPE>` placeholder value must come from the declared input or output `file_type`.
### Requirement: Station Context and Structured Execution

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Reserved key not shadowable
- **GIVEN** instance `definitions` containing a key that collides with a reserved station context name **WHEN** instance configuration is validated **THEN** startup is blocked or the invalid portion is rejected.

**Additional normative rules for this requirement:**

- **GIVEN** a station configuration containing a context reference that cannot be resolved **WHEN** the configuration is loaded **THEN** validation fails.
Context references in station configuration MUST resolve through the effective station context before dispatch: instance `definitions` are injected at the context root, reserved runtime keys must not be shadowed, full-node references may preserve scalar, sequence, or mapping types, embedded references must resolve to scalar strings, and unresolved or type-incompatible references are validation failures. Structured execution MUST define an executable and argument list; arguments are resolved through the same station context and passed as strings without shell interpretation by default. The context-reference grammar MUST NOT consume structured filename placeholders such as `<MISSION_ID>`.

- **GIVEN** a station declaring structured execution **WHEN** it is dispatched **THEN** the executable and argument list is resolved through the station context and executed without shell interpretation of arguments.
### Requirement: Execution Mode Resolution

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Default executor used
- **GIVEN** a station without `execution.mode` **WHEN** it is dispatched **THEN** the instance `executor.default` executor is used.

**Additional normative rules for this requirement:**

- **GIVEN** a station declaring `execution.mode: slurm-docker` that is registered **WHEN** the station is dispatched **THEN** the station command runs inside the station-declared Docker image from the SLURM job wrapper, and the effective configuration must declare a container image with all required shared roots mounted.
The station's `execution.mode` MUST name a type registered in the instance executor registry. Supported executor types are `slurm-native`, `slurm-docker`, `local`, and `stub`. If a station omits `execution.mode`, the instance `executor.default` is used. If the declared mode is not registered, dispatch MUST fail fatally with a clear error identifying the missing configuration and MUST NOT trigger automatic retry. Station `resources`, `slurm`, and `container` settings MUST be validated and merged with the matching instance executor defaults before dispatch.

- **GIVEN** a station declaring an execution mode absent from the executor registry **WHEN** the run is dispatched **THEN** the run and task fail with a fatal dispatch error identifying the missing type and suggesting adding it to instance configuration.
### Requirement: Station Revision Entity

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Revision recorded per run
- **GIVEN** a run being prepared **WHEN** preparation completes **THEN** the exact station revision used by the run is recorded durably in the run record.

**Additional normative rules for this requirement:**

- **GIVEN** a station definition changing in a way that may affect execution semantics **WHEN** the change is published **THEN** a new immutable station revision is created rather than mutating an existing one.
A station revision is the fully immutable frozen identity of a station configuration at the time a run is created. It MUST carry a revision identifier, station identifier, `station_name`, content hash or equivalent immutable revision marker, schema version, and effective timestamp or publication version. It MUST uniquely identify the effective configuration used by a run and MUST be referenced by job orders, runs, and processing fingerprints. If no valid station revision can be resolved, task preparation MUST fail.
### Requirement: Task Entity

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Waiting inputs join task
- **GIVEN** a join downstream task whose mandatory input set is incomplete **WHEN** no contributing upstream run has arrived **THEN** the task is parked in `waiting_inputs` and retried when another contributing upstream route arrives, rather than failing.

#### Scenario: Forced task recorded
- **GIVEN** a task submitted with the force flag set **WHEN** it is persisted **THEN** the task is recorded as forced so canonical and non-canonical runs remain distinguishable.

**Additional normative rules for this requirement:**

- **GIVEN** a task submission with `start` after `end` **WHEN** the task is validated **THEN** it is rejected before dispatch planning.
A task is the immutable routing request specifying a processing window and destination station. It MUST carry `task_id`, destination `station_id`, `start`, `end`, and routing history; it MAY carry a parent task identifier, parent run reference `(parent_task_id, parent_retry_index)`, split-group identifier, submission idempotency key, force flag (defaulting to `false` and persisted as such when omitted), client metadata, and priority. Routing content is immutable after creation; backend-maintained status summaries may change. A join downstream task MAY remain in the `waiting_inputs` parked lifecycle state before any run exists, meaning mandatory input resolution is incomplete and the task should be retried when another contributing upstream route arrives; this is not a terminal failure.

- **GIVEN** a task that is a member of a split group or an aggregation task for a split group **WHEN** it is created **THEN** it carries a split-group identifier; for ordinary tasks the identifier is optional.
### Requirement: Task Identifier Grammar

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Opaque timestamp segment
- **GIVEN** the configured task identifier timestamp source is `start` **WHEN** a task identifier is generated **THEN** the identifier's timestamp segment is not used by control-plane logic in place of the task's `start` field.

**Additional normative rules for this requirement:**

- **GIVEN** a task identifier containing a station prefix **WHEN** the task is persisted **THEN** the prefix matches the destination `station_id`.
The preferred external task identifier grammar is `stationid-YYYYMMDDThhmmssmillis-HEX6suffix` or another deployment-approved key shape with equivalent uniqueness. The full task identifier MUST be globally unique within the deployment scope; a `station_id` prefix, if used, MUST match the task's `station_id` destination field, and a bare `station_id` is not a valid task identity. The timestamp segment is generated from the configured instance policy (`creation` or `start`, defaulting to `creation`) and, when derived from the processing-window start, MUST still be treated as opaque identifier content; the authoritative processing window remains the task's `start` and `end` fields.

- **GIVEN** a client reading a task identifier **WHEN** interpreting the timestamp segment **THEN** it treats the segment as opaque and reads the window from `start` and `end`.
### Requirement: Submission Idempotency

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Idempotent retry returns same task
- **GIVEN** a prior successful submission under key K for content C **WHEN** the same key K and content C are submitted again **THEN** the original task is returned.

**Additional normative rules for this requirement:**

- **GIVEN** a client retrying a submission with the same key and same content **WHEN** the submission is processed **THEN** the same logical task is returned without creating a duplicate task.
A submission idempotency key identifies one client submission attempt within the configured API idempotency scope. Reusing the same key with the same submission content MUST return the same logical task result; reusing it with conflicting content MUST be rejected or reported as an idempotency conflict. Idempotency deduplicates exact client retries and MUST NOT merge merely similar tasks unless an explicit API policy defines matching rules. The backend MUST retain idempotency records for at least the documented retry window.

- **GIVEN** a client reusing a key with conflicting content **WHEN** the submission is processed **THEN** the request is rejected or reported as an idempotency conflict.
### Requirement: Routing History

Routing history is the append-only, human-readable record of stations or tasks previously traversed by the data, stored with the task. Existing entries MUST be immutable; new downstream tasks MAY append entries derived from the parent. Entries MUST remain ordered and internally consistent; if history cannot be extended consistently, downstream task creation MUST fail. Routing history is not the full provenance model.

#### Scenario: History consistency enforced
- **GIVEN** a downstream task creation whose derived history is inconsistent **WHEN** creation is attempted **THEN** downstream task creation fails.

**Additional normative rules for this requirement:**

- **GIVEN** a downstream task being created from a parent run **WHEN** routing history is derived **THEN** the child's history contains the parent's entries plus the new traversal entry, and the parent's entries are unchanged.
### Requirement: Provenance Links

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Provenance failure blocks finalization
- **GIVEN** a run finalization step that cannot persist a required provenance edge **WHEN** finalization proceeds **THEN** the step fails or remains incomplete rather than silently dropping the edge.

**Additional normative rules for this requirement:**

- **GIVEN** two upstream runs both contributing to one join task **WHEN** each run completes **THEN** each run has its own provenance link to the same join task.
A provenance link records an explicit immutable parent-child relationship between domain objects (tasks, runs, artifacts) with source object type and identifier, target object type and identifier, and relationship type; it MAY carry a contribution role and creation reason. At minimum, downstream task creation MUST create a provenance link from the producing parent run (referenced by `(task_id, retry_index)`) to each child task. For join downstream tasks, every contributing upstream run MUST create its own provenance link to the shared join task even if the task was already created by an earlier contributor. Each backend-recorded produced artifact MUST have a provenance link from the producing run to the artifact. Split-group membership MUST be represented by provenance or equivalent membership links from the split group to member child tasks and, when aggregation is materialized, to the aggregation task. Provenance links MUST be queryable independently of the task history text, and if required provenance edges cannot be persisted, the enclosing finalization step MUST fail or remain incomplete.

- **GIVEN** an artifact recorded by the backend **WHEN** it is persisted **THEN** a provenance link exists from the producing run, referenced by `(task_id, retry_index)`, to the artifact.
### Requirement: Job Order Entity

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Job order persisted with run
- **GIVEN** a run whose station renders a job order **WHEN** preparation completes **THEN** the job order is written under the working root and associated with the run record.

**Additional normative rules for this requirement:**

- **GIVEN** a station with `joborder.format: none` **WHEN** a run is prepared **THEN** no job-order file or artifact is produced and dispatch still has a frozen, auditable manifest and resolved execution arguments.
A job order is the immutable execution descriptor consumed by the station algorithm, created during run preparation after input resolution succeeds and written to the working root and associated with the run record. When `joborder.format` is `yaml`, `toml`, or `json`, the descriptor MUST be rendered in the station-declared format. A station MAY declare `joborder.format: none`, in which case no job order entity or artifact is created, and `VERIPROC_JOBORDER_PATH` MUST be omitted or unset; the resolved manifest plus resolved execution arguments MUST still be frozen and auditable before dispatch.

- **GIVEN** failed job order generation, context resolution, or validation **WHEN** run preparation completes **THEN** the run is not dispatched.
### Requirement: Job Order Reserved Names and Root Layout

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Reserved names protected
- **GIVEN** `joborder.include` containing key `task_id` **WHEN** the job order is generated **THEN** validation rejects the include because the legacy top-level VeriProc metadata name is reserved.

**Additional normative rules for this requirement:**

- **GIVEN** a default-renderer job order **WHEN** it is rendered **THEN** `order`, `inputs`, and `outputs` appear at the root and audit fields appear under `veriproc_meta`.
For the default renderer, workload-facing fields `order`, `inputs`, and `outputs` MUST remain at the joborder root, and VeriProc-owned audit and identity fields MUST be grouped under the generated `veriproc_meta` mapping unless `joborder.meta: false` is explicitly declared; at minimum `veriproc_meta` MUST contain schema version, task identifier, retry index, run reference, station identity, station revision identity, generator identity, and manifest reference when present. Station `joborder.include` may add non-reserved root fields such as `facility`, `dyn_params`, `temporary_folder`, `config_files`, and `log_level`, but MUST NOT override generated `veriproc_meta`, `order`, `inputs`, or `outputs` fields and MUST NOT reintroduce legacy top-level VeriProc metadata names (`schema_version`, `joborder_id`, `task_id`, `retry_index`, `run_ref`, `station`, `generator`, `manifest`). `joborder.include` is valid only with the default renderer; template renderer stations express custom values through `joborder.params` and the template body. If the job order includes a readable run reference such as `scen2-20250703T111239000-a7sf7f/r5`, it MUST be derived from `task_id` and `retry_index` and MUST NOT replace those fields. All VeriProc-resolved input, output, manifest, and joborder paths MUST resolve within the run's working-root execution namespace, though a station may request absolute path rendering when processors cannot resolve relative paths.

- **GIVEN** a station `joborder.include` declaration attempting to override `veriproc_meta` or a legacy top-level metadata name **WHEN** the job order is validated **THEN** generation fails.
### Requirement: Job Order Flat Inputs

The rules in this requirement are normative and SHALL be satisfied by the implementation.
Default job-order inputs SHOULD be represented as a flat list keyed by `file_type`. Each flat-list input entry may reference one filesystem object or multiple filesystem objects for the same `file_type`, and each object may be a regular file or a directory.

#### Scenario: Flat list keyed by file type
- **GIVEN** resolved inputs for `file_type` values A and B **WHEN** the job order is rendered **THEN** the `inputs` root field is a flat list with one entry per `file_type`.

**Additional normative rules for this requirement:**

- **GIVEN** a resolved input with multiple selected objects for one `file_type` **WHEN** the default-renderer job order is built **THEN** a single flat-list entry under that `file_type` references all selected objects.
### Requirement: Template Renderer

A station may use the built-in template renderer (`joborder.renderer: template`) with a template body, station-directory-relative template file, and resolved `joborder.params`. Template-rendered job orders MUST parse as the declared format, MUST be deterministic for the frozen run context, and MUST fail before dispatch if a required input, parameter, or helper call cannot be resolved. The default renderer is used when `joborder.renderer` is omitted.

#### Scenario: Deterministic template rendering
- **GIVEN** the same frozen run context rendered twice by the same template **WHEN** the outputs are compared **THEN** they are identical.

**Additional normative rules for this requirement:**

- **GIVEN** a template referencing an unresolvable parameter **WHEN** the job order is rendered **THEN** rendering fails before dispatch.
### Requirement: Resolved Input Manifest

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Manifest keyed by composite run identity
- **GIVEN** a frozen manifest for run `(T, r2)` **WHEN** it is persisted **THEN** it is queryable from the database by `task_id` T and `retry_index` 2, independent of the working-root export.

**Additional normative rules for this requirement:**

- **GIVEN** an input resolution where a mandatory input cannot be found **WHEN** the manifest is frozen **THEN** dispatch is prevented.
The resolved input manifest is the frozen record of which inputs were selected and which optional inputs were absent and why, persisted durably in database records keyed by `(task_id, retry_index)` with a serialized copy exported under the working root. It MUST contain task-scoped run identity (`task_id`, `retry_index`), one entry per declared input, and a selection outcome for each entry. All mandatory inputs MUST resolve successfully; missing mandatory inputs MUST prevent dispatch. Optional missing inputs MUST be marked explicitly. Each resolved input MUST retain its declared category and resolved folder source, may bind to one or several source filesystem objects with multiplicity preserved, and — when selected through filename parsing — MUST retain the effective `filename_pattern`, parsed filename metadata, applied interval-matching policy, logical interval grouping key, source folder priority, and winner comparison metadata that justified the selection. Directory inputs MUST satisfy the same effective naming contract as regular files by matching the directory entry name, and the materialized input symlink must point to the selected source directory root. The manifest is the source of truth for the inputs used or missing and must explain the choice without re-running folder search logic; the exported copy must include the same matching traceability metadata but must not replace the database records required for query and recovery.

- **GIVEN** structured filename matching selecting multiple objects for one declared input **WHEN** the manifest is recorded **THEN** those objects represent distinct logical interval groups and all are preserved in the entry.
### Requirement: Manifest Integrity and Checksum Policy

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Available-only checksum recording
- **GIVEN** an input with an available sidecar checksum **WHEN** the manifest is recorded **THEN** the checksum value, algorithm, and source are recorded, distinguishing the source.

**Additional normative rules for this requirement:**

- **GIVEN** a directory input under `available_only` policy **WHEN** the manifest is frozen **THEN** no recursive digest is computed and the input resolves without checksum metadata.
Under the default `available_only` integrity policy, manifest freezing MUST NOT require VeriProc to compute full-file checksums; checksums may be recorded from sidecars, upstream metadata, or external metadata when already available. Directory inputs MUST NOT require MD5 or other checksum metadata, and VeriProc MUST NOT walk directory contents solely to compute a recursive digest. Manifest entries MAY record checksum value, algorithm, and source when available under the instance integrity policy, with object kind supporting at least `regular_file` and `directory`.
### Requirement: Run Entity and Composite Identity

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Run requires parent task
- **GIVEN** a run creation attempt with no existing parent task **WHEN** validation runs **THEN** the run is not created.

**Additional normative rules for this requirement:**

- **GIVEN** one task with multiple attempts **WHEN** a retry is created **THEN** the new run uses a fresh `retry_index` under the same `task_id` and is persisted before dispatch.
A run is one execution attempt for one specific task and has no meaning without its parent task. Its canonical identity is `(task_id, retry_index)` where `retry_index` is a non-negative integer starting at `0`, scoped to the parent task, and not globally unique. A run MUST reference an existing task, MUST have a valid state transition path, MUST use the next valid `retry_index` (or a retry-safe documented attempt-allocation rule), MUST NOT reuse an existing `(task_id, retry_index)` pair, and MUST be persisted before dispatch is considered successful. The preferred display form is `<task_id>/r<retry_index>` (for example `scen2-20250703T111239000-a7sf7f/r5`); this is a presentation convention that MUST NOT be treated as a durable identifier or parsed beyond its two API-provided components. Implementation status: the implementation uses an internal unique `run_id` surrogate key for physical storage and, at present, as the primary REST API and CLI resource identifier; the composite key remains the conceptual identity and `run_ref` display form, and migrating API/CLI addressing to the composite key is tracked as a Known Issue.

- **GIVEN** an operator viewing a run **WHEN** a display reference is rendered **THEN** it is generated as `<task_id>/r<retry_index>` and not stored as a new identifier.
### Requirement: Run Retry Isolation

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Distinct working roots per run
- **GIVEN** two runs of the same task **WHEN** their working roots are materialized **THEN** the roots are distinct and cannot overwrite each other's retry artifacts, manifests, logs, or task output descriptors.

**Additional normative rules for this requirement:**

- **GIVEN** a failed run `(T, r0)` with a recorded failure reason **WHEN** the task is retried as `(T, r1)` **THEN** run `(T, r0)`, its working root, manifest, fingerprint, and failure reason remain unchanged and inspectable.
A retry MUST create a new run under the same task with a new `retry_index` and MUST NOT mutate the original run attempt, its working root, its manifest, or its processing fingerprint. Each run owns a distinct working root, and retrying a task must not reuse or overwrite a prior run's working root. The run failure reason records why an attempt reached a failed or cancelled terminal state; retries MUST keep independent failure reasons so operators can inspect an earlier failed run even after a later retry is running or completed. Forced runs, ordinary retries, duplicate attempts, and recovery-created attempts all use the same `(task_id, retry_index)` identity model. A failed run remains part of the durable audit history for its parent task.
### Requirement: Run Canonicality and Downstream Routing

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Forced rerun routes downstream
- **GIVEN** a forced task whose fingerprint is already canonically owned **WHEN** its run completes successfully **THEN** downstream tasks are created.

**Additional normative rules for this requirement:**

- **GIVEN** a successful non-canonical run **WHEN** finalization completes **THEN** downstream tasks are created from it.
Every successfully completed run triggers downstream task creation REGARDLESS of canonicality classification; canonicality is an audit label on the run and deduplication record, not a downstream routing gate. Duplicate detection happens before dispatch: if a fingerprint is already canonically owned at preparation time and the task is not forced, the task is failed before any execution occurs and no downstream tasks are created from it. Forced runs and runs that reach execution through a concurrent race always route downstream on success. The backend decides canonical or non-canonical classification during finalization using the processing fingerprint, force flag, and deduplication policy. Run lifecycle states and reconciliation behavior are defined by the run state model in the architecture chapter; the current state model has no dedicated `orphaned` state (see Known Issues).

- **GIVEN** a task whose fingerprint has an existing canonical owner and no force flag **WHEN** preparation runs **THEN** the task fails with a duplicate classification before execution and no downstream tasks are created.
### Requirement: Job Entity

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Scheduler identifier persisted when known
- **GIVEN** a job record created before scheduler assignment **WHEN** the execution adapter obtains the scheduler identifier **THEN** the identifier is persisted durably with the job record.

**Additional normative rules for this requirement:**

- **GIVEN** a run submitted with uncertain scheduler acknowledgment **WHEN** submission completes **THEN** the run enters reconciliation or orphan handling rather than being silently dropped.
A job is the concrete scheduler submission for a run, attached to exactly one task-scoped run and created when the execution adapter submits the run. It MUST carry the run reference `(task_id, retry_index)` and submission timestamp; it MAY carry scheduler identifier, queue/partition, account and QoS, effective executor mode, resource request, scheduler-native state and diagnostics, wrapper log paths, and cancellation timestamp. A persisted job record MAY be created before the scheduler identifier is known, but once the adapter obtains a scheduler identifier it becomes required and MUST be persisted durably as soon as it is known. A job may have its own globally unique `job_id`, but that does not make the run globally identified by a standalone run identifier. If scheduler acknowledgment is uncertain, the run MUST enter reconciliation or orphan handling. Reconciliation compares persisted run and job records with scheduler-visible state after uncertainty or restart; orphan handling is a run lifecycle condition, not a separate job entity.
### Requirement: Runtime Environment Contract

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Mandatory variables present at launch
- **GIVEN** a dispatched run **WHEN** the algorithm process starts **THEN** all mandatory `VERIPROC_*` variables for that run are set with values consistent with the job order content.

**Additional normative rules for this requirement:**

- **GIVEN** a run with `joborder.format: none` **WHEN** the process environment is assembled **THEN** `VERIPROC_JOBORDER_PATH` is absent.
The runtime environment is the set of environment variables injected into the algorithm process immediately before scheduler submission or job launch. VeriProc-defined variables MUST include `VERIPROC_WORKING_ROOT`, `VERIPROC_RUN_DIR`, `VERIPROC_STATION_ID`, `VERIPROC_TASK_ID`, `VERIPROC_RETRY_INDEX`, `VERIPROC_RUN_REF`, `VERIPROC_WINDOW_START`, `VERIPROC_WINDOW_END`, `VERIPROC_STATION_DIR` (when the station executable path has a containing directory), and `VERIPROC_JOBORDER_PATH` (when a joborder file is generated; omitted or unset when `joborder.format` is `none`). Optional variables include scheduler-native variables, `VERIPROC_JOB_ID` when a job record identifier is available before launch, operator-defined variables from top-level instance `execution_env`, and debug or tracing variables. Instance `execution_env` variables MUST NOT use the reserved `VERIPROC_` prefix and MUST NOT override VeriProc-defined variables. VeriProc-defined paths MUST resolve within the working root namespace, injection MUST remain consistent with job order content, the effective environment for a run is immutable once the job starts, and missing mandatory variables MUST fail dispatch preparation or be treated as an execution-adapter defect. Window variables are derived from the task window after validation and UTC normalization; `VERIPROC_RUN_REF` is derived from `task_id` and `retry_index` using the preferred display convention. The documented environment contract must be versioned by implementation policy, and environment variables should be treated as execution-layer process state, not mandatory serialized job-order fields.

- **GIVEN** an instance `execution_env` key starting with `VERIPROC_` **WHEN** instance configuration is validated **THEN** validation fails.
### Requirement: Working Root Entity

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Working root path not an identity
- **GIVEN** a working-root path containing station, task-window, retry, or timestamp components **WHEN** other entities reference the run **THEN** those components are never used as foreign keys, provenance targets, API identities, or deduplication owners.
the template may produce one or multiple path components under the configured working-root base. Generator identity is recorded as an opaque label when declared, with the system-default marker otherwise; implementation status: external generator-module execution is not implemented in the current baseline. An expanded path that is absolute or would escape the working-root base is deterministically rewritten into a safe nested layout under the base rather than failing preparation (Known Issue: fail-fast is the proposed correction). Partially created working roots must be recoverable or cleaned up deterministically; implementation status: unowned working roots created before the run record committed are not currently detected by startup reconciliation (Known Issue).

- **GIVEN** an expanded working-root path escaping the base **WHEN** the path is assigned **THEN** it is deterministically rewritten into a safe nested layout under the base.

#### Scenario: Generator identity recorded
- **GIVEN** an instance declaring a working-root generator identity **WHEN** a run is prepared **THEN** the opaque label is recorded in run metadata.

**Additional normative rules for this requirement:**

- **GIVEN** two runs whose expanded working-root paths would collide **WHEN** the paths are generated **THEN** a deterministic disambiguation suffix is appended to the final path component.
The working root is the isolated filesystem workspace for exactly one run, created during run preparation and located under an allowed shared-storage root. It MUST be unique for the run, MUST NOT collide with another run, and MUST contain the required internal execution layout — including an `input/` directory with symlinks to the resolved input filesystem objects, where a symlink may target a regular file or the root of a selected source directory — plus an exported copy of the input manifest. The path MUST be deterministic for the validated run context and the effective instance naming policy, generated from an effective relative path template composed from normalized station, task, and run segments unless a conforming custom generator is configured. If the readable expanded path can collide, the generator MUST append a deterministic disambiguation suffix to the final path component without reordering existing readable components. the template may produce one or multiple path components under the configured working-root base, and task-window timestamp components should use compact UTC millisecond precision while run-event timestamp components may use compact UTC microsecond precision. Generator identity is recorded as an opaque label when declared, with the system-default marker otherwise; implementation status: external generator-module execution is not implemented in the current baseline. An expanded path that is absolute or would escape the working-root base is deterministically rewritten into a safe nested layout under the base rather than failing preparation (Known Issue: fail-fast is the proposed correction). Partially created working roots must be recoverable or cleaned up deterministically; implementation status: unowned working roots created before the run record committed are not currently detected by startup reconciliation (Known Issue).

- **GIVEN** a run's resolved inputs **WHEN** the working root is materialized **THEN** `input/` contains symlinks to each selected object, including directory roots for directory inputs.
### Requirement: Task Output Descriptor

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Descriptor recorded in run
- **GIVEN** a run completing with a valid task output descriptor **WHEN** finalization succeeds **THEN** the descriptor path and validated content summary are recorded in the run.

**Additional normative rules for this requirement:**

- **GIVEN** a malformed `task-out.yaml` **WHEN** the run finalizes **THEN** finalization fails unless explicit fallback policy is configured.
The task output descriptor (`task-out.yaml`) is written by the algorithm before completion and declares downstream tasks; it is treated as immutable once read for finalization. It MUST validate against the descriptor schema, target stations must exist, duplicate entries must be rejected or normalized by explicit policy, and split-group or aggregation declarations must be validated against station policy before they affect group membership, closure, readiness, or aggregation routing. Malformed descriptors MUST fail finalization unless explicit fallback policy is configured. The file path and validated content summary should be recorded in the run. The downstream effects of the descriptor are governed by the producing run's completion state: every successfully completed run triggers downstream task creation regardless of canonicality. Split-group metadata in `task-out.yaml` is authoritative only after the backend validates and accepts it under the station's dynamic split-declaration policy. If a run is identified as a duplicate before dispatch, it is failed without execution and no downstream tasks are created from it.

- **GIVEN** a valid `task-out.yaml` and a station policy of `reject` for dynamic split declarations **WHEN** the descriptor contains split-group metadata **THEN** the declarations are rejected and do not affect group state.
### Requirement: Split Group Definition

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Closure irreversible
- **GIVEN** a closed split group **WHEN** a new child declaration arrives **THEN** membership cannot be extended and the declaration is rejected or handled by duplicate policy.

**Additional normative rules for this requirement:**

- **GIVEN** a fan-out whose children later need grouped evaluation **WHEN** the splitter run declares downstream fractional work **THEN** a split group with the identity fields above is created.
A split group coordinates a grouped fan-out/fan-in processing set derived from one parent window. Fan-out creates member child tasks; fan-in evaluates those members for readiness and, when the group uses aggregation semantics, materializes the aggregation stage. The group MUST carry `group_id`, parent task or run reference, parent time window, group evaluation mode (`aggregation` or `completion_only`), grouped evaluation target or stage, membership basis, closure state, readiness state, grouped evaluation state, and readiness policy. Identity and parent association are immutable; membership may change only until closure, which is irreversible; readiness and grouped-evaluation states may change until terminal state. A split group is mandatory for any fan-out whose children must later be evaluated together. The default membership model is task-based: the group contains child tasks, not individual runs; runs are considered through the canonical completion result of each child task. A child task MUST belong to at most one active split group unless an explicit multi-group membership policy is defined.

- **GIVEN** a completion-only group **WHEN** readiness is reached **THEN** readiness or failure is recorded without creating an aggregation task.
### Requirement: Split Group Membership and Closure

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Duplicate child normalization
- **GIVEN** duplicate child declarations in a fan-out **WHEN** membership is being closed **THEN** the duplicates are rejected or normalized by deterministic policy.

**Additional normative rules for this requirement:**

- **GIVEN** a membership basis of expected child count only **WHEN** station configuration is validated **THEN** it is rejected as insufficient without a child-key derivation rule.
The group MUST define how child membership is determined: by an explicit child task set, by an expected child count plus a deterministic child-key derivation rule (expected count alone is not sufficient; the policy must define the child-key space, for example indexed sub-window keys), or by a dynamic membership predicate with an explicit closure event. The group MUST define when membership is closed and which actor may close it (backend during parent-run finalization, a valid closure declaration from `task-out.yaml`, or an explicit operator/API action allowed by policy). Closure is valid only if recorded membership satisfies the membership basis and duplicate-handling policy. Duplicate child declarations MUST be rejected or normalized according to a deterministic policy before membership is closed. Readiness MUST NOT be evaluated against an open-ended group unless the readiness policy explicitly supports partial aggregation after timeout.
### Requirement: Split Group Readiness and Aggregation

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Aggregation not started early
- **GIVEN** a closed group with readiness still false **WHEN** a child task completes **THEN** no aggregation task is created by that completion alone.

**Additional normative rules for this requirement:**

- **GIVEN** all members of an aggregation-mode group terminal with at least one canonical completion **WHEN** readiness is evaluated **THEN** readiness is true and at most one aggregation task is created.
Readiness MUST be evaluated per `group_id` under a deterministic readiness policy covering membership basis, completion criterion, failure handling, timeout behavior, and canonicality basis; child tasks from different groups must not be merged merely because they share station identity, product type, filename pattern, or overlapping time ranges. Readiness is evaluated over child tasks and their canonical completion results by default; each child task contributes at most one canonical completion result even with multiple runs. When partial aggregation is enabled, failed terminal children may be excluded per policy, but at least one canonical completed contribution is required for successful readiness. Under the default bounded partial policy, every member reaching terminal success or failure re-evaluates readiness; a group with expected member count and all members terminal completes when one or more canonical child results exist and fails when none exist. Aggregation MUST NOT start until the group is closed, readiness evaluates to true, and the evaluation mode is `aggregation`. Completion of an individual child task MUST NOT directly create an aggregation task unless that group's readiness has already evaluated to true. The backend MUST create at most one aggregation task per split group under the default policy; additional aggregation tasks require an explicit re-aggregation operation or documented replacement policy. For aggregation mode, the split-group policy MUST identify the aggregation target station or stage, which is part of the group definition rather than ordinary per-child routing. If a child task is retried, readiness waits for canonicality resolution unless the readiness policy uses another completion basis. If the system cannot determine readiness safely, the group MUST NOT be marked ready and aggregation MUST NOT start; if a mandatory child fails and exclusion or partial aggregation is not allowed, the group remains not ready or transitions to failed per policy.

- **GIVEN** a child task retried after readiness evaluation **WHEN** the retry is in flight **THEN** readiness waits for that child's canonicality to be resolved.
### Requirement: Split Group Aggregation Inputs

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Aggregation via run contract
- **GIVEN** a materialized aggregation run **WHEN** the station executes **THEN** its inputs are reachable through manifest entries, job-order references, and `input/` symlinks, not through archive scanning.

**Additional normative rules for this requirement:**

- **GIVEN** an aggregation run being prepared **WHEN** its input manifest is built **THEN** it lists exactly the canonical child artifacts selected, with contributing child task and producing run references.
Aggregation input selection MUST be defined by policy; the default input set is the validated output artifacts — or validated published artifacts when publication is required — from the canonical completed run of each contributing child task. The backend MUST materialize the aggregation input set from split-group membership and each contributing child task's canonical completed result. The aggregation station MUST NOT discover contributing sub-windows or sub-granules by unconstrained archive scanning or by parsing routing-history text. The backend MUST expose selected aggregation inputs through the normal run contract: resolved manifest entries, job-order input references, and symlinks under the aggregation run's `working_root/input/`. The aggregation run's manifest MUST record the exact child artifacts selected, including contributing child task identity and producing run reference `(task_id, retry_index)` where available. Canonicality changes after an aggregation task has been materialized MUST NOT retroactively alter that aggregation or its input set unless explicit re-aggregation policy is invoked. Aggregation outputs must remain attributable to the aggregation run that produced them while retaining links back to the split group and contributing child results. Re-aggregation requires an explicit operation or policy and MUST preserve the earlier aggregation record.

- **GIVEN** a child run becoming canonical after the aggregation task was materialized **WHEN** no re-aggregation is invoked **THEN** the aggregation and its input set are unchanged.
### Requirement: Split Group Source of Policy

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Dynamic declarations authoritative after validation
- **GIVEN** a station policy accepting dynamic split declarations and a valid `task-out.yaml` with split metadata **WHEN** finalization validates the declarations **THEN** they become authoritative for membership and closure.

**Additional normative rules for this requirement:**

- **GIVEN** a station with split-group-capable default downstream routing **WHEN** station configuration is validated **THEN** it defines deterministic membership, closure, readiness, grouped-evaluation mode, and aggregation rules.
Fan-out may be declared by `task-out.yaml` or by station default downstream configuration. When a valid `task-out.yaml` exists, it is authoritative for the downstream tasks and split-group membership produced by that run unless station policy explicitly allows fallback or merge behavior. When station default downstream configuration creates a split group without `task-out.yaml`, the station configuration is the source of split policy and MUST provide enough deterministic split metadata (membership, closure, readiness, grouped-evaluation rules, aggregation target and input-selection rules when applicable) to construct those behaviors from the parent task and run context. Stations accepting `task-out.yaml` split declarations MUST define whether declarations are accepted, rejected, or merged with station defaults, and how validated declarations become authoritative.
### Requirement: Artifact Entity

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Publication recorded without identity change
- **GIVEN** an artifact published into a rolling archive **WHEN** publication is recorded **THEN** the target archive, target path, publication mode, and source artifact are stored and the producing run reference is unchanged.

**Additional normative rules for this requirement:**

- **GIVEN** a completed run missing a mandatory output artifact **WHEN** finalization is evaluated **THEN** the run cannot reach `complete`.
An artifact is any persisted filesystem object associated with a run, including outputs, logs, the job order, the task output descriptor, and manifest exports; artifacts may be regular files or directories. The artifact identity, producing run `(task_id, retry_index)` or external origin reference, logical artifact type, path, and object kind are immutable; validation metadata may be added during finalization. Paths MUST remain within allowed storage policy; mandatory output artifacts MUST satisfy station validation rules; missing mandatory artifacts MUST prevent the run from reaching `complete`. Directory output artifacts MUST satisfy object-level validation (existence, expected object kind, effective name or naming-rule compliance, and explicit station policy) and baseline validation MUST NOT require recursive inspection of directory children. Artifact checksum metadata is optional unless station or deployment integrity policy requires it for the applicable object kind; the record MUST distinguish VeriProc-computed checksums from sidecar/upstream/external sources; directory artifacts must not require MD5 or other checksums in the baseline and VeriProc must not walk directory contents solely for a recursive digest. If copied or linked into a rolling archive, the publication target and mode MUST be recorded without changing the producing run identity.

- **GIVEN** a directory output artifact **WHEN** it is validated **THEN** validation checks existence, object kind, and naming compliance without recursive child inspection.
### Requirement: Processing Fingerprint

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Pre-dispatch duplicate detection
- **GIVEN** an unforced task whose fingerprint already has a canonical owner **WHEN** preparation computes the fingerprint **THEN** preparation fails, the task transitions to failed with a duplicate classification, and no run is dispatched.

**Additional normative rules for this requirement:**

- **GIVEN** two runs with identical scientific context where one is forced **WHEN** fingerprints are computed **THEN** the fingerprints differ because the force flag is included (current implementation behavior).
The processing fingerprint is the deduplication and canonicality key for a scientifically meaningful execution context, computed during run preparation after the manifest, station revision, configuration, generator identities, and output-affecting runtime parameters are frozen, and before the run is eligible for dispatch. It MUST include station identity, station revision identity, processing window, resolved input identities, resolved category-folder sources when they can affect reproducibility, generator identities when custom generation affects execution context, and output-affecting runtime parameters; it MAY include checksums, facility mode, algorithm image digest, job order schema version, and rolling archive source identifiers when archive selection can affect reproducibility. It is immutable once computed and MUST be stored with every prepared run whether the run becomes canonical or not (implementation status: the current implementation also includes the task `force` flag in the fingerprint value, so a forced rerun of otherwise-identical context always produces a distinct fingerprint — Known Issue: forced reruns cannot be fingerprint-compared against the canonical owner). If the fingerprint cannot be computed, the run MUST NOT be dispatched. If the fingerprint's canonical owner is already set at preparation time and the task is not forced, preparation MUST fail and the task MUST transition to failed state with a duplicate classification. Forced tasks create runs that are non-canonical by default, do not compete for the canonical deduplication record, still trigger downstream creation on success, and may be promoted to canonical only through explicit operator action, API action, or documented automatic replacement policy.

- **GIVEN** a prepared run that never becomes canonical **WHEN** its record is inspected **THEN** its computed fingerprint is retained on the run or an associated fingerprint record.
### Requirement: Deduplication Record

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Conflicting ownership fails
- **GIVEN** two attempts to claim canonical ownership for one fingerprint **WHEN** the conflict is detected **THEN** the transaction fails with operator-visible diagnostics.

**Additional normative rules for this requirement:**

- **GIVEN** a run reaching canonical completion **WHEN** finalization runs **THEN** the deduplication record is created or updated before canonical downstream effects are emitted.
The deduplication record identifies which completed run currently owns a canonical processing fingerprint. It MUST carry the processing fingerprint (immutable), the owning run reference `(task_id, retry_index)`, and canonical status; it MAY carry a superseded-by reference and promotion timestamp. Only one canonical owner MAY exist for a fingerprint at a time. Records are created or updated during finalization after output validation succeeds and before canonical downstream effects are emitted. A record's canonical status MUST be consistent with the owning run's canonical classification. Non-canonical runs retain their fingerprints but do not own canonical deduplication records. Supersession or promotion MUST be initiated by an explicit operator action, API action, or documented automatic policy and MUST record both the previous owner and the replacing owner. Conflicting ownership MUST fail transactionally and trigger operator-visible diagnostics. Records MUST be queryable independently of raw run records.
### Requirement: Facility Entity

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Facility in fingerprint
- **GIVEN** a facility field affecting scientific outputs **WHEN** the processing fingerprint is computed **THEN** that field is included per deployment policy.

**Additional normative rules for this requirement:**

- **GIVEN** facility data failing the job order schema **WHEN** job order generation runs **THEN** generation fails and the run is not dispatched.
Facility describes the operational context passed to the algorithm, derived from deployment configuration during job order generation. It MUST carry a processing environment identifier and MAY carry acquisition station, processing center, and a test flag. It is immutable within a run, must satisfy the job order schema, and invalid facility data MUST fail job order generation. It is recorded in the job order and therefore indirectly in the run context; facility fields that can affect scientific outputs MUST be included in the processing fingerprint according to deployment policy.
### Requirement: State Store

The state store is the relational persistence layer for all durable control data, enforcing referential integrity and uniqueness where required for idempotency. Loss of write availability MUST block state transitions that require durable commit. SQLite may be used for development; Postgres should be used for production.
- **GIVEN** the state store unavailable for writes **WHEN** a state transition requires durable commit **THEN** the transition is blocked.

#### Scenario: Uniqueness for idempotency
- **GIVEN** concurrent attempts to claim a unique key required by idempotency **WHEN** both are committed **THEN** referential integrity and uniqueness enforcement allow exactly one.
### Requirement: Instance Configuration

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Invalid configuration blocks startup
- **GIVEN** an invalid instance configuration **WHEN** the backend starts **THEN** startup is blocked or runs depending on the invalid portion cannot be created.

**Additional normative rules for this requirement:**

- **GIVEN** an instance configuration change after a run completed **WHEN** the run's semantics are revisited **THEN** the run references its frozen effective configuration and is unaffected.
Instance configuration defines deployment-level settings shared by all stations and runs. It MUST define the instance identifier, working root base path, station configuration root or registry reference, global station context definitions through `definitions` (including facility defaults when used), input category definitions through `product_categories`, a naming policy for generated path and filename segments, and an integrity policy for checksum handling; it MAY define rolling archives, declared working-root and job-order generator identities, an executor registry with per-executor configurations, global runner environment variables through `execution_env`, and retention/cleanup defaults. The effective instance configuration used by a run MUST be frozen by reference or content hash, and future changes MUST NOT retroactively alter existing run semantics; the effective configuration identity MUST be recorded with every run.
### Requirement: Instance Configuration Validation

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Integrity policy default
- **GIVEN** an instance configuration omitting the integrity policy **WHEN** it is loaded **THEN** the effective policy is `available_only`.

**Additional normative rules for this requirement:**

- **GIVEN** `execution_env` containing a key starting with a digit **WHEN** instance configuration is validated **THEN** validation fails.
Input category names declared through `product_categories` MUST be unique, rolling archive identifiers MUST be unique, and referenced paths MUST be allowed shared-storage paths. The optional `definitions` section MUST be a YAML mapping whose top-level keys do not collide with reserved station context names (`station_id`, `station_name`, `task_id`, `retry_index`, `run_ref`, `job_id`, `start`, `end`, `working_root`, `joborder`). The optional `execution_env` section MUST be a flat string mapping with keys that are valid environment variable names, do not start with a digit, and do not use the reserved `VERIPROC_` prefix. Custom generator identity declarations, when present, are opaque strings recorded in run metadata; implementation status: external generator-module loading and execution are not implemented in the current baseline (Known Issue / future extension). The task identifier timestamp source MUST be either `creation` or `start`, defaulting to `creation` when omitted. The integrity policy MUST be one of `none`, `available_only`, or `required` (or a documented equivalent with the same semantics) and MUST default to `available_only`. The naming policy MUST define timestamp encodings, task identifier timestamp source, the working-root relative path template and station/task/run segment templates, generated filename segment rules, allowed characters, unsafe-character replacement, station-case handling, collision-suffix behavior, and maximum segment length; it must not define alternate durable identifiers. If `execution_env` variables can affect station outputs, their effective values MUST be treated as part of the frozen execution context for audit and fingerprint policy. `definitions` supplies deployment-wide values for station context references (such as `<facility.processing_center>`) and does not itself create joborder fields; `execution_env` supplies deployment-wide process environment variables and does not itself create joborder fields or station context references.

- **GIVEN** a `definitions` key colliding with `retry_index` **WHEN** instance configuration is validated **THEN** validation fails.
### Requirement: Generation Hook

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Hook identity immutable per run
- **GIVEN** a run preparation in progress **WHEN** generator identities are assigned **THEN** they are immutable for that run thereafter.

**Additional normative rules for this requirement:**

- **GIVEN** an instance declaring a `joborder` generator identity **WHEN** a run is prepared **THEN** the opaque label is recorded in run metadata (no external module is loaded in the current baseline).
A generation hook identity labels the effective generator used for working-root path generation or job-order generation, requiring a hook type (`working_root` or `joborder`) and an implementation version or identity label. Implementation status: the current baseline records declared generator identities as opaque strings in run metadata (`veriproc_meta` and run generator maps) but does not load or execute external generator modules; module execution is a future extension tracked in Known Issues. The hook identity used for a run is immutable once run preparation begins and MUST be recorded in run metadata or generated artifacts when it can affect execution context. Future extension contract (not yet implemented): hook modules must implement the required interface, be deterministic for the same validated inputs, not mutate backend state directly, preserve working-root containment and job-order schema validity, and their failure must fail run preparation before scheduler submission.
### Requirement: Rolling Archive Entity

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Publication failure holds finalization
- **GIVEN** a run whose output publication into a rolling archive fails **WHEN** finalization is evaluated **THEN** the run remains in finalization or fails per station policy, and no downstream tasks are created from unpublished data.

**Additional normative rules for this requirement:**

- **GIVEN** a mandatory input available only through an unavailable archive **WHEN** input resolution runs **THEN** resolution fails and dispatch is prevented.
A rolling archive is a named read-write directory used as operational short-term storage for input selection and optional publication of validated outputs, declared in top-level instance configuration with archive identifier, filesystem path, and retention period; it may carry mode/permissions, publication subdirectory policy, checksum policy, cleanup schedule, and an allowed station list. The archive identifier must be stable within an instance; path and retention are deployment-managed and change only through controlled configuration updates. Paths MUST be visible to backend and compute nodes if used for symlinked input access, and station references such as `rolling:co2m_hot` MUST resolve to a declared archive. Unavailable archives MUST fail input resolution if needed for mandatory inputs. Publication failure after successful job execution MUST keep the run in finalization or fail according to station policy and MUST NOT silently create downstream tasks that depend on unpublished data. Input manifest entries MUST record the archive identifier when an object is selected from a rolling archive, and publication records MUST record target archive, target path, publication mode, and source artifact.
### Requirement: Executor Entity

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Startup validation of executors
- **GIVEN** an invalid executor registry entry **WHEN** the backend starts **THEN** startup validation of that entry fails.

**Additional normative rules for this requirement:**

- **GIVEN** a registered executor whose scheduler connection cannot run commands **WHEN** health or dispatch is evaluated **THEN** the dependency is reported as unavailable.
The executor is the adapter that translates runs into compute jobs. The instance configuration MUST define an executor registry under `executor.executors`, each entry mapping a type name to adapter configuration, with the optional `executor.default` naming the entry used when a station declares no `execution.mode`. Supported type names are `slurm-native`, `slurm-docker`, `local`, and `stub`; implementations may accept `plain-slurm` and `docker-slurm` as input aliases, but persisted job records SHOULD use the canonical names. Each registered executor configuration MUST be validated at startup, station-level executor overrides must be validated before a station revision can dispatch, and the adapter MUST verify that the configured connection can run scheduler commands or report the dependency as unavailable through health and dispatch errors. Submission uncertainty MUST be surfaced as reconciliation or orphan handling, not silently ignored. Scheduler terminal failures (timeout, cancellation, node failure, non-zero process exit) MUST be preserved as diagnostics and mapped to the executor state model, and failure to write working-root logs or exit-code evidence from the wrapper MUST be surfaced as executor failure or reconciliation uncertainty. Effective executor type, scheduler identifier, scheduler-native state, submission attempt, and diagnostic metadata needed for reconciliation MUST be persisted with the job record or retrievable through recorded artifacts; executor-specific ephemeral state is not the system of record.

- **GIVEN** an alias `plain-slurm` in input configuration **WHEN** the job is persisted **THEN** the job record uses the canonical name `slurm-native`.
### Requirement: Shared Storage

Shared storage is the filesystem namespace accessible to both backend and compute nodes, provisioned as part of the environment. It MUST provide the consistency and path visibility required by the working-root contract. Storage unavailability during preparation or finalization MUST fail the affected transition and be recoverable on restart. Artifact bytes live here; control truth does not.

#### Scenario: Control truth independent of storage
- **GIVEN** shared storage contents for a run **WHEN** authoritative run state is needed **THEN** it is read from the state store, not reconstructed from storage bytes.

**Additional normative rules for this requirement:**

- **GIVEN** shared storage unavailable during finalization **WHEN** the transition is attempted **THEN** it fails and can be recovered on restart.
### Requirement: Glossary Distinctions

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Distinct identities across entity types
- **GIVEN** job, artifact, split-group, station-revision, and processing-fingerprint identifiers for one workflow **WHEN** they are stored **THEN** each remains a distinct canonical identifier for its own concept and is not used as an identity for another entity type.

**Additional normative rules for this requirement:**

- **GIVEN** a station overriding folder search order via `rolling_folders` **WHEN** configuration is loaded **THEN** the top-level `product_categories` definitions remain authoritative and no new global categories are introduced.
The domain vocabulary MUST remain distinct: station (declarative processing-step specification), station revision (frozen version of a station definition), task (immutable routing request for a processing window), routing history (readable append-only traversal log carried by tasks), provenance link (explicit graph edge between tasks, runs, or artifacts), job order (immutable execution descriptor consumed by the algorithm), resolved input manifest (frozen record of selected and missing inputs), run (one execution attempt for one task), job (concrete scheduler submission for a run), runtime environment (predefined environment variables injected into the job process), working root (isolated filesystem workspace for one run), `task-out.yaml` (optional descriptor declaring downstream tasks), split group (coordination record for fan-out/fan-in workflows), artifact (persisted file associated with a run), processing fingerprint (deduplication key for scientific execution context), deduplication record (canonical ownership record for a fingerprint), facility (operational context block passed to the algorithm), state store (relational persistence layer), instance configuration (deployment-level configuration), `product_categories` (top-level ordered category definitions that determine folder search precedence), `rolling_folders` (station-level override for category folder search order), generation hook (declared identity labeling the working-root or job-order generation strategy; external module execution is a future extension), rolling archive (named operational directory for input selection and optional output publication), executor (adapter that submits and tracks compute jobs), and shared storage (POSIX-visible filesystem for working roots and artifacts) are separate concepts that MUST NOT be conflated. In particular, `product_categories` defines global category definitions and search precedence and MUST NOT be redefined by `rolling_folders`, which is only a station-level override of folder search order for declared input categories.
