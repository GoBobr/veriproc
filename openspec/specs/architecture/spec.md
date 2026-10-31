# Architecture Specification

## Purpose

This capability specifies the system architecture of VeriProc as normative requirements derived from the former source specification chapter 2, "System Architecture Overview" (source document removed; see git history). It covers the three architectural layers (control plane, execution description layer, execution layer), authoritative records, versioning boundaries, instance and station configuration, run lifecycle, dispatch, output validation, publication, downstream routing, deduplication, and recovery. Diagrams referenced by these requirements live in the appendix of `docs/docs/02-architecture.md`.

## Requirements

### Requirement: Architectural layers and authorities

The system SHALL be organized into three layers: a control plane that accepts tasks, validates them, resolves inputs, generates job orders, dispatches jobs, validates outputs, creates downstream tasks, and serves the API; an execution description layer that freezes the context of one run into a versioned job order plus a resolved input manifest

#### Scenario: Layer responsibilities
- **GIVEN** a deployed VeriProc instance
- **WHEN** a task is submitted and processed to completion
- **THEN** control state is persisted in the database, artifact bytes live on the shared filesystem, runtime status comes from the scheduler, and the algorithm writes only inside its working root

**Additional normative rules for this requirement:**

and an execution layer that translates a run into a concrete scheduler job and exposes sufficient status for the control plane to track completion.
The database MUST be authoritative for control state, the shared filesystem for artifact bytes, the scheduler for job admission and runtime status, and the science algorithm only for files it writes inside the working root.

### Requirement: Control-plane background loop cadence
The dispatcher loop MUST admit unprepared accepted tasks, dispatch ready runs, poll active runs, and finalize runs whose jobs reached a terminal state. A conforming implementation SHOULD use an adaptive cadence: the tick interval MAY back off when a pass finds no work and MUST return to the base cadence as soon as work is found. Station pause state SHOULD be resolved in batch per pass rather than per run.

#### Scenario: Idle backoff
- **GIVEN** a dispatcher pass that finds no work
- **WHEN** the next tick is scheduled
- **THEN** the tick interval backs off from the base cadence while remaining bounded

#### Scenario: Work found resumes cadence
- **GIVEN** a dispatcher in backed-off cadence
- **WHEN** a subsequent pass finds work
- **THEN** the tick interval returns to the base cadence immediately

#### Scenario: Batched pause resolution
- **GIVEN** multiple stations with pending pause state
- **WHEN** a dispatcher pass executes
- **THEN** pause state is resolved once per pass in batch, not once per run

### Requirement: High-level component responsibilities

The backend MUST validate submissions, persist task/run state, compute fingerprints, generate job orders, dispatch jobs, recover after restart, validate outputs, and create downstream tasks.
The state store MUST persist tasks, task relationships, runs, jobs, manifests, artifacts, split groups, and deduplication records.
The execution adapter MUST submit, query, and (when supported) cancel jobs through a stable adapter contract.

#### Scenario: Client interaction boundary
- **GIVEN** a client frontend
- **WHEN** it submits or queries tasks
- **THEN** it uses the API exclusively and does not write directly into working roots

**Additional normative rules for this requirement:**

Shared storage MUST hold working roots, logs, outputs, optional task-out descriptors, and symlinked input views.
The station configuration store MUST provide versioned station revisions validated before use.
Client frontends MUST interact only through the API and MUST NOT mutate backend state by writing directly into working roots.

### Requirement: Authoritative records

The implementation MUST maintain the following records: task record (immutable routing request plus status summary), task relationship record (parent-child and provenance edges), run record (one execution attempt per task), job record (scheduler-facing, linked to one run), station revision record (frozen configuration identity), resolved input manifest (frozen input selection and metadata)

#### Scenario: Manifest audit export
- **GIVEN** a completed run preparation
- **WHEN** the working root is inspected
- **THEN** a serialized audit copy of the resolved input manifest exists under the working root while the database remains the system of record for manifest query and recovery

**Additional normative rules for this requirement:**

job order (frozen execution descriptor when the station enables a joborder file), artifact record (logical and physical identity of produced filesystem objects), rolling archive definition (instance configuration), rolling archive publication record, split-group record, and deduplication record (canonical completed fingerprint ownership).

The database MUST remain the system of record for query and recovery even where records are stored redundantly in the working root. The working-root copy of the resolved input manifest is a required audit export and MUST NOT replace the database records required for manifest query and recovery.

### Requirement: Versioning boundaries

Every station configuration MUST resolve to a station revision.
Every default-rendered job order that emits `veriproc_meta` MUST carry a job order schema version; processor-specific template joborders MAY use their own schema contract.
Declared generator identities MUST be recorded as opaque implementation-identity strings in run metadata; loader/execution semantics for external generator modules are NOT implemented in the current baseline.

#### Scenario: Station edit isolation
- **GIVEN** a run created with station revision R
- **WHEN** the station configuration is later edited
- **THEN** the meaning of that run is not retroactively changed

**Additional normative rules for this requirement:**

The API SHOULD expose an explicit API version, and database schema changes MUST follow an explicit migration policy.

### Requirement: Instance configuration pre-flight validation
The top-level instance configuration is not a station definition; it defines deployment-specific roots and extension points. A conforming implementation MUST validate it before startup, including storage roots, naming and generated path policy, integrity policy, rolling archives, an executor registry of named adapter configurations, global definitions, and optional execution environment variables, and MUST expose its effective identity for audit.

#### Scenario: Invalid instance configuration
- **GIVEN** an instance configuration that violates this specification
- **WHEN** the backend starts
- **THEN** startup fails with a validation error before any run is processed

### Requirement: Default generator selection

If no generator identity is declared, the system-default generators MUST be used.
Declared generator identities are opaque strings recorded under `veriproc_meta` and run metadata for audit; the current baseline does not load or execute external generator modules.

#### Scenario: No declared generator
- **GIVEN** an instance configuration without a `generators` section
- **WHEN** working roots and job orders are generated
- **THEN** the system-default generators are used

**Additional normative rules for this requirement:**

A future module-loading contract, if defined, MUST provide custom generators only validated inputs (task, run identity, station revision, resolved manifest, resolved station context, and instance storage settings), MUST require deterministic results, and MUST forbid direct database mutation.

### Requirement: Definitions section semantics

The optional top-level `definitions` section is an arbitrary nested YAML mapping of deployment-wide values injected at the root of the station configuration reference context.
Nested dot-path references MUST be supported for mapping keys.
Referenceable path segments MUST use ASCII letters, digits, and underscores, with the first character of the first segment lowercase or `_`.

#### Scenario: Missing definition key
- **GIVEN** a station referencing `<facility.processing_center>` where `facility` is not defined
- **WHEN** station validation or run preparation runs
- **THEN** the reference fails before dispatch

#### Scenario: Embedded scalar reference
- **GIVEN** a scalar string `mode-<generic_mode>` with `generic_mode: NOMINAL` under definitions
- **WHEN** the context is resolved
- **THEN** the result is the string `mode-NOMINAL`

**Additional normative rules for this requirement:**

A reference to a missing key, to a path traversing through a non-mapping node, or to a reserved runtime context name that conflicts with `definitions` MUST fail station validation or run preparation before dispatch.
A full-node reference MAY resolve to a scalar, sequence, or mapping and preserves the native YAML type; embedded references inside a larger string MUST resolve to scalar values rendered as strings.
Effective resolved values used by a run MUST be frozen before processing fingerprint computation and dispatch.

### Requirement: Execution env section rules
The optional top-level `execution_env` section is a flat YAML mapping of operator-defined environment variables exported to every station runner process. Variable names MUST be valid shell environment names, MUST NOT start with a digit, and MUST NOT use the reserved `VERIPROC_` prefix. Values are strings after YAML decoding. Execution environment variables MUST be injected alongside, but MUST NOT override, the VeriProc-defined runtime variables.

#### Scenario: Reserved prefix rejected
- **GIVEN** `execution_env` containing a variable named `VERIPROC_FOO`
- **WHEN** instance configuration is validated
- **THEN** validation fails

### Requirement: Naming policy scope

The top-level `naming` policy governs generated working-root relative paths and path components, the task identifier timestamp source, and any VeriProc-generated filenames or filename-adjacent segments.
It MUST NOT create alternate durable identities.

#### Scenario: Unsafe character replacement
- **GIVEN** a generated path segment containing a character outside the allowed charset
- **WHEN** the segment is generated
- **THEN** the configured replacement character is applied and the result stays within the maximum segment length

**Additional normative rules for this requirement:**

Generated path components and filename segments MUST be deterministic for the validated input context, MUST use only the configured allowed character set after normalization, and MUST apply the configured replacement rule for unsafe characters.
Implementations SHOULD enforce a maximum segment length, with 128 characters recommended unless deployment policy requires otherwise.

### Requirement: Task id timestamp source
The optional `naming.task_id_timestamp` setting controls the timestamp segment used by the preferred external task identifier grammar. Supported values are `creation` and `start`; if omitted, it MUST default to `creation`. `start` means the segment is derived from the task processing-window start time. The configured source affects only task identifier generation; the authoritative creation timestamp and processing window remain persisted task fields and API response fields.

#### Scenario: Default timestamp source
- **GIVEN** an instance configuration without `naming.task_id_timestamp`
- **WHEN** a task identifier is generated
- **THEN** the timestamp segment is derived from task creation time

### Requirement: Working-root path template

The system-default working-root generator MUST use `naming.working_root.path_template` as a relative path template under `storage.working_root_base`.
The only defined placeholders are `{station}`, `{task}`, and `{run}`, expanding to the normalized results of `station_segment`, `task_segment`, and `run_segment` respectively; other placeholders are invalid unless this specification defines them.

#### Scenario: Flat template
- **GIVEN** `path_template: "{task}-{run}"`
- **WHEN** a working root is generated
- **THEN** a single relative component is produced under the working-root base

#### Scenario: Escaping path rewritten
- **GIVEN** a template expansion that would escape the working-root base
- **WHEN** the working root is generated in the current implementation
- **THEN** the path is deterministically rewritten into a safe nested layout under the base instead of failing preparation

**Additional normative rules for this requirement:**

The final path may be one or multiple components under `working_root_base`.
The template MUST be relative, not absolute; after expansion and normalization the expanded path MUST NOT escape `working_root_base`, MUST NOT contain empty path components, and MUST NOT rely on `.` or `..` semantics.

Implementation status: in the current implementation, an expanded path that is absolute or would escape the base is not rejected with a preparation failure; instead it is deterministically rewritten into a safe nested layout under `working_root_base` (tracked as a Known Issue with a proposal to fail preparation instead).

### Requirement: Working-root collision suffix

If the expanded path would collide with an existing run path, `collision_suffix` MUST append an additional deterministic suffix to the final expanded path component only.
Collision disambiguation MUST NOT alter the ordering of readable components produced by the path template and segment templates, and the suffix MUST NOT be treated as a second identifier.

#### Scenario: Collision suffix append
- **GIVEN** an existing run path with final component `STATION-A-20260508T122736733-868ba9-r0`
- **WHEN** a second logical run generates the same readable component
- **THEN** the new component becomes `STATION-A-20260508T122736733-868ba9-r0-<deterministic-suffix>` without reordering existing tokens

**Additional normative rules for this requirement:**

The suffix MUST be deterministic for the same validated run context and MUST NOT be a fresh random value on each generation attempt; it MAY be derived from a stable per-run uniqueness component such as a short internal run key.

### Requirement: Structured filename pattern contract

The top-level `naming.filenames.filename_pattern` defines the instance-wide structured filename contract; it is a parsing and rendering template, not only a glob.
The pattern language MUST support at least the placeholders `<MISSION_ID>`, `<FILE_TYPE>`, `<START_TIME>`, `<END_TIME>`, and `<GENERATION_TIME>`, plus an unconstrained suffix or wildcard segment when needed.
The placeholder vocabulary MUST remain available to station-level overrides.

#### Scenario: Station pattern override
- **GIVEN** a station output declaring its own `filename_pattern`
- **WHEN** output validation runs for that file type
- **THEN** the station-local pattern overrides the instance-wide default, while component semantics remain the global definitions

#### Scenario: FILE_TYPE substitution
- **GIVEN** an input declaring `file_type: SCE_2__CNF____AX` and an effective pattern containing `<FILE_TYPE>`
- **WHEN** candidates are scanned
- **THEN** the placeholder is substituted from the declared file type before scanning

**Additional normative rules for this requirement:**

The instance MUST define component rules for each supported placeholder under `naming.filenames.components`; these rules are global and define fixed semantics such as width, allowed character class, and timestamp format (for example `MISSION_ID` as `CDM?`, `FILE_TYPE` of 16 ASCII characters, and timestamp components of the fixed UTC shape `YYYYMMDDTHHmmSSZ`).
Station-level definitions MAY override the overall `filename_pattern` but MUST NOT redefine the global component semantics.

A station input or output `filename_pattern` overrides the instance-wide default for that declared file type. When an effective pattern contains `<FILE_TYPE>`, VeriProc MUST substitute it from the declared `file_type` and MUST NOT infer it from candidate discovery alone. During input selection and output validation the backend MUST parse candidate filenames according to the effective pattern and extract the declared structured components.

### Requirement: Fixed positional component extraction

Structured-component extraction MUST use fixed positional extraction derived from the effective `filename_pattern` and the globally defined component rules, with externally visible behavior equivalent to fixed-width, position-based extraction rather than heuristic splitting.
The implementation MAY internally compile this into helper code, tables, or regexes.

#### Scenario: Positional FILE_TYPE parse
- **GIVEN** a candidate name and a `FILE_TYPE` component of length 16
- **WHEN** the name is parsed
- **THEN** exactly 16 ASCII characters are consumed at the declared position

**Additional normative rules for this requirement:**

A `MISSION_ID` configured as `CDM?` MUST match four ASCII characters whose first three positions are `C`, `D`, `M` and fourth is any ASCII character.
A `FILE_TYPE` configured with length 16 MUST consume exactly 16 ASCII characters at its declared position.
Timestamp components configured as `YYYYMMDDTHHmmSSZ` MUST be parsed from exactly 16 characters and interpreted as UTC.

### Requirement: Compact timestamp formatting

Timestamp segments generated by VeriProc MUST use compact UTC formatting without punctuation.
Task-window timestamps MUST NOT require more than millisecond precision (for example `20250703T101218019Z`).
Runtime event timestamps such as run creation, preparation, dispatch observation, or finalization MAY use microsecond precision (for example `20260506T071500001234Z`).

#### Scenario: Runtime event timestamp
- **GIVEN** a run creation event
- **WHEN** its timestamp is rendered into a generated name
- **THEN** it uses compact UTC formatting with at most microsecond precision

**Additional normative rules for this requirement:**

RFC 3339 timestamps MAY still be used in API payloads and persisted timestamp fields; compact formatting applies to generated path components, generated filenames, and CLI examples displaying those names.

### Requirement: Integrity checksum policy

The top-level `integrity` policy governs checksum recording for input manifests, artifacts, and publication metadata.
The default policy MUST be `available_only`: checksums are recorded from sidecars, upstream metadata, or external metadata when already available, but large regular files MUST NOT be read solely to compute checksums.
A policy of `none` means checksums are neither required nor computed.

#### Scenario: Available only default
- **GIVEN** the `available_only` policy and a resolved input with no sidecar checksum
- **WHEN** the manifest is recorded
- **THEN** the file is not read to compute a checksum and the checksum field remains optional

**Additional normative rules for this requirement:**

A policy of `required` means checksums for regular files MUST exist or be computed according to deployment or station policy before the relevant validation step succeeds.
Directory inputs and directory artifacts do not require checksum validation in the baseline, and VeriProc MUST NOT walk directory contents solely to compute a recursive digest.
Manifest and artifact checksum fields remain optional unless policy explicitly requires them for the applicable object kind.

### Requirement: Executor registry

The top-level `executor` section defines a registry of named execution adapters.
Each entry under `executor.executors` is identified by a type name and holds deployment-level adapter configuration.
The optional `executor.default` names the entry used when a station declares no `execution.mode`; if no default is set, every station MUST explicitly declare its mode.
Supported type names are `slurm-native`, `slurm-docker`, `local`, and `stub`.

#### Scenario: Missing executor fatal error
- **GIVEN** a station declaring `execution.mode: slurm-docker` with no `slurm-docker` entry in the registry
- **WHEN** the run is dispatched
- **THEN** the run and task fail with a fatal, non-retriable dispatch error identifying the missing configuration

#### Scenario: Legacy executor form
- **GIVEN** an instance configuration with a single executor configuration directly under `executor:`
- **WHEN** the backend starts
- **THEN** the configuration is accepted as the legacy form and used as the sole executor

**Additional normative rules for this requirement:**

Implementations MAY accept `plain-slurm` as an alias for `slurm-native` and `docker-slurm` as an alias for `slurm-docker`, but canonical names in persisted records and documentation SHOULD be `slurm-native` and `slurm-docker`; the current implementation accepts only the canonical names.

For backward compatibility, the implementation also accepts a legacy single-executor form in which `executor:` directly contains one executor configuration instead of an `executors` map, and the `VERIPROC_EXECUTOR` environment variable may override the selected executor at daemon startup. The registry form is preferred for new deployments.

If a station requests an executor type that is not registered in `executor.executors`, the backend MUST fail the run and task with a fatal, non-retriable dispatch error that clearly identifies the missing configuration.

### Requirement: Executor types

The rules in this requirement are normative and MUST be satisfied by the implementation.
The `local` executor runs the station executable as a direct OS process on the backend host, requires no additional registry configuration, and is suitable for testing or where the backend host is the processing node.
The `stub` executor simulates the job lifecycle without running any script and is intended for integration testing only.

#### Scenario: Station override merge
- **GIVEN** an instance executor with default partition `unlim` and a station override `partition: zen3_1024`
- **WHEN** a run is prepared
- **THEN** the effective partition is `zen3_1024` and all other defaults are inherited deterministically and frozen before dispatch

**Additional normative rules for this requirement:**

For SLURM deployments, each entry MUST contain the connection method used to run scheduler commands (local execution on a submit host or SSH to a SLURM login node) and scheduler defaults such as account, partition, QoS, command paths, poll interval, default CPU, memory, and walltime limits, and the shared filesystem roots visible to the backend, submit host, compute nodes, and containers.

Station-level configuration MAY override scheduler defaults only for that station (partition, QoS, account, CPUs per task, memory, walltime, extra `sbatch` arguments, and container image). Station overrides MUST be merged with the matching instance executor defaults deterministically and frozen into the run's execution context before dispatch. In `slurm-docker`, the container image is station-specific (declared in `station.yaml`); the instance entry MAY supply Docker defaults such as bind mounts, user mapping, network policy, and cleanup policy, but MUST NOT silently substitute a different algorithm image once the station revision has been selected.

### Requirement: Station configuration model

Stations are declared in YAML and MUST define enough information for scheduling, input resolution, output validation, and downstream routing.

#### Scenario: Invalid station rejected
- **GIVEN** a station configuration missing a required field such as `station_id`
- **WHEN** station configuration is validated
- **THEN** the revision is rejected and cannot be used to create runs

**Additional normative rules for this requirement:**

At minimum a station revision MUST include: `station_id`; `station_name`; execution image and mode; structured execution command; resource requirements; joborder renderer configuration; category-folder overrides when overriding top-level category defaults; input definitions including declared `file_type`, category, and any `filename_pattern` override or interval-matching policy
output definitions including declared `file_type`, any `filename_pattern` override, and whether multiple matching filesystem objects may be validated; per-output publication configuration when outputs are published to a rolling archive; default downstream routing; split-group policy when default routing can create grouped fan-out/fan-in work; and any station-specific validation hooks or runtime parameters.
The backend MUST validate station configurations before accepting them for dispatch; invalid station revisions MUST NOT be used to create runs.

### Requirement: Station context references
Station configuration MAY contain context references in scalar string values using syntax `<name>` or `<name.path>` with dot-separated mapping traversal. Referenceable path segments MUST use ASCII letters, digits, and underscores with the first character of the first segment lowercase or `_`. Scalar fields with their own angle-bracket grammar, such as `filename_pattern`, MUST NOT be processed by the station context resolver.

#### Scenario: Embedded reference requires scalar
- **GIVEN** a scalar string embedding a reference that resolves to a mapping
- **WHEN** context resolution runs
- **THEN** the substitution is rejected as type-incompatible

#### Scenario: Reserved key collision
- **GIVEN** instance `definitions` defining a top-level key `task_id`
- **WHEN** configuration is validated
- **THEN** validation fails because `task_id` is a reserved run-preparation key

**Additional normative rules for this requirement:**

The context root consists of all top-level keys from instance `definitions` plus reserved run-preparation keys supplied by VeriProc: `station_id`, `station_name`, `task_id`, `retry_index`, `run_ref`, `job_id` when available, `start`, `end`, `working_root`, `joborder`, and `joborder.path` when a joborder file is generated. Instance `definitions` MUST NOT define top-level keys that collide with these reserved names.

A scalar string that is exactly one reference MAY resolve to a scalar, list, or mapping. A scalar string embedding one or more references inside other text MUST resolve every reference to a scalar string value. Resolution MUST occur after task validation, station revision selection, working-root assignment, joborder target determination, and input manifest finalization, and before the effective station execution context is frozen. Unresolved references, type-incompatible substitutions, or references to values unavailable at the point of use are validation failures. Resolved values that affect command arguments, generated joborder content, output declarations, or other output-affecting behavior are part of the frozen execution context and MUST be included in processing fingerprint computation per the fingerprint policy.

### Requirement: Split-group policy declaration

If a station revision can create a split group through default downstream routing, its configuration MUST define the grouped workflow policy deterministically, including: the group evaluation mode; grouped evaluation target or stage; membership basis; child-key derivation or explicit child set rules; closure policy; readiness policy; failure and partial-aggregation behavior; and aggregation target and input-selection rules when aggregation is enabled.

#### Scenario: Split group definition without policy
- **GIVEN** a station whose default downstream routing can create split groups but declares no grouped workflow policy
- **WHEN** station validation runs
- **THEN** the configuration is rejected

**Additional normative rules for this requirement:**

If split-group metadata is emitted dynamically through `task-out.yaml`, the station configuration MUST define whether dynamic split declarations are allowed and which fallback or merge behavior applies when both `task-out.yaml` and default downstream routing are present.

### Requirement: Downstream routing modes

Default downstream routing MUST support the modes `normal`, `task_out`, `fan_in`, and `join`.
`normal` creates a downstream task immediately after the producing run finalizes.
`task_out` authorizes a target that must be instantiated by a validated algorithm-produced `task-out.yaml`.
`fan_in` creates downstream work by split-group completion.

#### Scenario: Join barrier
- **GIVEN** two upstream stations with join routes sharing `join_id`, target station, and processing window
- **WHEN** both contributing runs complete
- **THEN** one downstream task is created with provenance from both runs and held in `waiting_inputs` until all mandatory inputs resolve

**Additional normative rules for this requirement:**

`join` is for independent upstream stations that all contribute mandatory inputs to the same downstream station: a join route MUST declare `join_id`, and all upstream routes participating in the same barrier MUST use the same `join_id`, target station, and processing window.
The backend MUST derive a deterministic downstream task identity from those values, record provenance from every contributing run, and park the task in `waiting_inputs` until the target station's mandatory input declarations can all resolve.

### Requirement: Station execution mode binding

Station `execution.mode` names one of the executor types registered in the instance `executor.executors` map.
If a station omits `execution.mode`, the instance `executor.default` MUST be used; if `execution.mode` names an unregistered type, the backend MUST fail the run and task with a fatal dispatch error identifying the missing configuration.

#### Scenario: Omitted execution mode
- **GIVEN** a station without `execution.mode` and an instance `executor.default: slurm-native`
- **WHEN** the station's run is dispatched
- **THEN** the `slurm-native` executor is used

**Additional normative rules for this requirement:**

A station using `slurm-native` runs `execution.executable` and resolved `execution.args` directly in the SLURM job wrapper; `slurm-docker` runs them inside the declared Docker image from the same wrapper; `local` runs the executable as a direct OS process on the backend host.
Station resource declarations are algorithm requirements; effective SLURM submission parameters are obtained by overlaying station resources and station `slurm` overrides on the matching instance executor defaults.

### Requirement: Task/run/job/joborder cardinality

The rules in this requirement are normative and MUST be satisfied by the implementation.
A task is an immutable request to process a destination station over a time window; a run is one execution attempt for that task; a job is the scheduler submission that carries out that run; a job order is the immutable execution document generated for that run when the station's `joborder.format` is not `none`.

#### Scenario: Retry creates new run
- **GIVEN** a failed run with `retry_index: 0`
- **WHEN** the task is retried
- **THEN** a new run with `retry_index: 1` is created and the original run identity is unchanged

**Additional normative rules for this requirement:**

The cardinality rules are: one task may have zero or more runs; one run MUST reference exactly one task; one run may have zero or one active scheduler jobs at a time; one job MUST reference exactly one run; and one run MUST reference zero or one generated job order, referencing none only when the station explicitly declares `joborder.format: none`.
Retries MUST create new runs, not mutate the identity of an existing run.

### Requirement: Run identity is task-scoped

The rules in this requirement are normative and MUST be satisfied by the implementation.
The canonical run key is `(task_id, retry_index)`, where `retry_index` starts at `0` for the first attempt and increments for later attempts created by retry, force, recovery, or other documented attempt-creation policy.

#### Scenario: Canonical run key
- **GIVEN** task `scen2-20250703T111539000-a7sf7f` and its first attempt
- **WHEN** the run is referenced at the API level
- **THEN** it is identified by the composite `(task_id, retry_index=0)`

**Additional normative rules for this requirement:**

Run identity is intentionally task-scoped: implementations MAY use an internal unique run key for physical storage, but the task-scoped composite remains the conceptual and API-level run identity.

### Requirement: Task model and submission

A task MUST contain at minimum: a task identifier; destination station identity through `station_id`; `start` and `end` timestamps; routing history; and provenance links to its parent task or parent run when applicable.
A task is immutable after creation except for backend-maintained status metadata.
Task creation time is task metadata, not part of the minimum routing window definition.

#### Scenario: Idempotent resubmission
- **GIVEN** a client request with idempotency key `client-req-7f84d0` already accepted
- **WHEN** the same request is resubmitted
- **THEN** the same logical result is returned without creating a second task

#### Scenario: Identifier prefix mismatch
- **GIVEN** a task identifier prefixed `scen1-` whose destination is station `scen2`
- **WHEN** the task is submitted
- **THEN** the task is rejected because the prefix must match the destination station

**Additional normative rules for this requirement:**

A task identifier MAY include the destination `station_id` as an operator-readable prefix, but the full task identifier MUST remain globally unique within the deployment scope.
The preferred external task identifier grammar is `stationid-YYYYMMDDThhmmssmillis-HEX6suffix`, with the timestamp segment generated from `naming.task_id_timestamp`.
The `station_id` prefix in a task identifier MUST match the task's destination `station_id`; a bare `station_id` is not a valid task identity.
Re-submitting the same client request with the same idempotency key MUST return the same logical result.
Downstream tasks created by the framework are new domain objects, not mutable copies of the parent, and parent-child relationships MUST be persisted explicitly even if the task carries a readable history list.
For aggregation tasks created from a split group, the readable history list SHOULD include the ancestral routing chain, the fan-out parent run, and the contributing split-group member runs selected or excluded by the readiness policy; this history is an operator-facing projection, and membership and provenance links remain the authoritative machine-queryable representation.

### Requirement: Input resolution determinism
Input resolution MUST be deterministic and auditable. The baseline input searcher is the classical matching algorithm, which applies the same decision path to every declared input regardless of input category or object kind: it scans all configured source folders, parses candidate entry names through the effective structured filename contract, filters candidates by the declared input and window policy, groups candidates by logical interval, and selects one deterministic winner per group.

#### Scenario: Category-independent algorithm
- **GIVEN** a product input and an auxiliary input with the same declarations except `file_type`
- **WHEN** inputs are resolved
- **THEN** both follow the same decision path, and only the folder list differs by category

### Requirement: Input selection rules
The backend MUST resolve inputs according to the following rules:

#### Scenario: Winner tie-break by generation time
- **GIVEN** two candidates in the same logical interval group with different parsed generation times
- **WHEN** the winner is selected
- **THEN** the candidate with the most recent real generation time is selected

#### Scenario: Sentinel ranks below real generation time
- **GIVEN** one candidate with sentinel generation time `yyyymmddTHHMMSS` and one with a real parsed generation time in the same group
- **WHEN** the winner is selected
- **THEN** the candidate with the real generation time is selected

#### Scenario: Full folder scan
- **GIVEN** a candidate that satisfies the window policy in a lower-priority folder
- **WHEN** selection completes
- **THEN** every configured folder was scanned and folder priority affected only tie-breaking, not early exit

**Additional normative rules for this requirement:**

- Each declared input MUST specify a `category` and a `file_type`.
- Candidate filesystem objects (regular files or directories) MUST first satisfy the effective `filename_pattern` for that input: the station-local pattern when present, otherwise the instance-wide default.
- If the effective pattern contains `<FILE_TYPE>`, it MUST be substituted from the declared input `file_type` before candidate scanning or validation.
- Candidate names satisfying the pattern MUST be parsed into structured metadata using fixed positional extraction; at minimum the backend MUST extract `mission_id`, `start_time`, `end_time`, `generation_time`, a post-generation discriminator or suffix when the pattern declares one, and `file_type` when the pattern contains `<FILE_TYPE>`.
- The effective folder list for an input MUST be taken from the matching top-level `product_categories` entry; a station MAY override the folder list for a category through `rolling_folders`.
- Folder entries referencing `rolling:<archive_id>` MUST be resolved through instance configuration before scanning.
- Resolved folder entries MUST preserve their declared order relative to literal filesystem paths; this order is the user-priority order, with earlier folders having higher priority.
- Candidate interval matching MUST be evaluated against the effective processing window using the declared `window_match` policy: supported policies MUST include `overlaps`, `within_window`, and `covers_window`; compatibility aliases such as `cross`, `fully_within`, and `surrender` MAY be accepted, but canonical names are `overlaps`, `within_window`, and `covers_window`. (Legacy product-style within-window behavior maps to `within_window`; full-cover behavior maps to `covers_window`; legacy auxiliary-style overlap behavior maps to `overlaps`.)
- If a station definition declares time margins, those margins MUST expand the effective processing window before `window_match` evaluation.
- The matcher MUST scan every configured folder before final selection; folder priority MUST NOT be used as an early-exit boundary.
- After filtering, candidates MUST be grouped by logical interval identity: the declared input `file_type` together with the parsed candidate start and end timestamps. A declared input may therefore resolve to zero, one, or multiple selected filesystem objects when multiple logical intervals match.
- Each logical interval group may yield at most one selected candidate.
- The default group winner rule MUST prefer, in order: the candidate with the most recent real parsed generation time; then the lexicographically greatest parsed post-generation discriminator or suffix; then the candidate from the highest-priority source folder (earliest configured); then a final stable fallback such as the lexicographically smallest full source path.
- A configured sentinel representing unknown generation time (such as `yyyymmddTHHMMSS`) MUST rank below any real parsed generation time.
- Modification time MUST NOT be used as the primary structured-filename tie-break when the effective filename contract provides generation time and discriminator metadata.

### Requirement: Half-open interval semantics

All interval comparisons MUST treat time intervals as half-open: the start timestamp is inclusive and the end timestamp is exclusive.
A candidate whose end timestamp equals the window start (or whose start equals the window end) shares only a zero-duration boundary point and MUST NOT be considered overlapping; this rule applies uniformly to all `window_match` policies.

#### Scenario: Boundary point is not overlap
- **GIVEN** a candidate interval ending exactly at the processing window start
- **WHEN** `overlaps` matching is evaluated
- **THEN** the candidate does not match

#### Scenario: Covers window
- **GIVEN** a candidate interval strictly containing the processing window
- **WHEN** `covers_window` is evaluated
- **THEN** the candidate matches

**Additional normative rules for this requirement:**

The canonical semantics are: `overlaps` matches candidates with `candidate_end > window_start AND candidate_start < window_end`; `within_window` matches candidates with `candidate_start >= window_start AND candidate_end <= window_end`; `covers_window` matches candidates with `candidate_start <= window_start AND candidate_end >= window_end`.
Interval policies are input-level policy choices, not category-specific algorithms.

### Requirement: Input filter rules
A station input MAY declare a `filter` list of named filter rules. Filter rules MUST be applied after window-match filtering and before interval grouping and winner selection. Each filter rule MUST reduce or preserve the surviving candidate set and MUST NOT introduce new candidates. Rules are applied in declaration order and a candidate MUST pass all declared rules to survive.

#### Scenario: Filename component filter
- **GIVEN** a declared `filename_component` filter with `component: mission_id` and `source_file_type` whose winner has `mission_id: CDMA`
- **WHEN** the filter runs
- **THEN** candidates whose parsed `mission_id` is not `CDMA` (case-insensitive) are dropped

#### Scenario: Filter eliminates mandatory input
- **GIVEN** a mandatory input whose entire candidate set is eliminated by a filter rule
- **WHEN** input resolution completes
- **THEN** the input resolves to no winners with a selection reason identifying the filter rule, and run preparation fails

**Additional normative rules for this requirement:**

The `filename_component` rule is the first supported filter rule. It MUST accept a `component` name and a `source_file_type` name. For each surviving candidate, the rule MUST compare the candidate's parsed value for the named component (case-insensitive) against the value of the same component extracted from the first already-resolved winner of the declared `source_file_type` within the same manifest resolution pass. Candidates whose component value does not match MUST be dropped. If no winner for `source_file_type` has been resolved when the filter runs, the rule MUST be skipped and all current candidates survive unchanged. If the source winner does not carry the named component in its parsed metadata, the rule MUST be skipped with a logged warning and all current candidates survive unchanged.

If all candidates for an input are eliminated by a filter rule, the input MUST resolve to no winners, and the selection reason MUST identify the filter rule that eliminated all candidates. A mandatory input with no survivors MUST cause run preparation to fail.

### Requirement: Recorded manifest metadata

For each resolved or missing input, the manifest MUST record: file type; category; mandatory or optional status; source path if resolved; source directory precedence; object kind if resolved (at least `regular_file` and `directory` supported); size if resolved and meaningful for the object kind; modification time if resolved; checksum value, algorithm, and checksum source if available under the instance integrity policy; effective filename pattern

#### Scenario: Audit export completeness
- **GIVEN** a resolved input selected from multiple candidates
- **WHEN** the exported working-root manifest is read
- **THEN** it contains the same selection metadata as the database, including the winner comparison keys

**Additional normative rules for this requirement:**

applied `window_match` policy; parsed mission identifier when resolved; parsed start, end, and generation timestamps when resolved; parsed file type when the effective pattern contains `<FILE_TYPE>`; parsed post-generation discriminator or suffix when used by the effective pattern; logical interval identity or equivalent grouping metadata when structured filename matching is used; candidate source folder priority when resolved
any version or revision metadata derived from filename or sidecar metadata; and the selection reason or tie-break outcome including the winner comparison keys that determined the selected candidate when multiple candidates were considered.

The working-root copy of the manifest MUST preserve the same selection metadata persisted in the database and MUST be sufficient for an operator to understand why each selected object won without re-running directory scans.

### Requirement: Frozen execution context
The manifest MUST be frozen before dispatch. Once the run reaches the `ready` state, the resolved input set MUST NOT change for that run.

#### Scenario: Manifest immutability after ready
- **GIVEN** a run in state `ready`
- **WHEN** new candidate files appear in source folders
- **THEN** the run's resolved input set remains unchanged

### Requirement: Job order evaluation preconditions

The backend MUST evaluate the station `joborder` section only after task validation, station revision resolution, working-root assignment, and input manifest finalization succeed.
The normative station key is `joborder`; `job_order` is not the canonical spelling.
For formats other than `none`, the backend MUST generate a job order only after context resolution succeeds, and the job order MUST be written to the working root before scheduler submission.

#### Scenario: Generation failure blocks submission
- **GIVEN** job order generation that fails during template execution or format validation
- **WHEN** run preparation completes
- **THEN** no scheduler job is submitted for that run

**Additional normative rules for this requirement:**

If job order generation fails, no scheduler job may be submitted.

### Requirement: Job order formats

The system-default joborder renderer MUST support exactly these station-declared formats: `yaml` (YAML document), `toml` (TOML document with equivalent structured content), `json` (JSON document with equivalent structured content), and `none` (no joborder file and no joborder artifact).
If `joborder.format` is omitted, it MUST default to `yaml`.

#### Scenario: Default format and name
- **GIVEN** a station `joborder` section with no `format` or `name`
- **WHEN** the job order is generated
- **THEN** a `joborder.yaml` file is written inside the working root

#### Scenario: None format contract
- **GIVEN** a station declaring `joborder.format: none`
- **WHEN** the run is prepared and dispatched
- **THEN** no joborder file or artifact is created, `VERIPROC_JOBORDER_PATH` is not injected, and arguments do not reference `<joborder>`

**Additional normative rules for this requirement:**

If `joborder.name` is omitted and the format is not `none`, the default filename MUST be `joborder.yaml`, `joborder.toml`, or `joborder.json` according to the format.
`joborder.name` MUST be a relative filename or relative path inside the working root, MUST NOT be absolute, and MUST NOT escape the working root after normalization.
If `joborder.format` is `none`, `joborder.name` and `joborder.include` MUST be absent or empty, `VERIPROC_JOBORDER_PATH` MUST NOT be injected, and station execution arguments MUST NOT reference `<joborder>` or `<joborder.path>`; `none` is an explicit station contract that no serialized joborder is presented to the algorithm, while the resolved manifest, station revision, run record, and command arguments still form a frozen auditable execution context.

### Requirement: Job order renderers

The rules in this requirement are normative and MUST be satisfied by the implementation.
The station `joborder.renderer` field selects the rendering backend, defaulting to `default` (the VeriProc-owned structured joborder document, supporting `joborder.include`).
A station MAY set `joborder.renderer: template` to render a station-specific joborder from a Go `text/template` body supplied inline through `joborder.template` or loaded from a station-directory-relative `joborder.template_file`.

#### Scenario: Template format validation
- **GIVEN** a template renderer station with `joborder.format: toml`
- **WHEN** the rendered output does not parse as TOML
- **THEN** job order generation fails before dispatch

#### Scenario: Include with template renderer rejected
- **GIVEN** a station declaring `joborder.renderer: template` together with `joborder.include`
- **WHEN** station configuration is validated
- **THEN** the configuration is rejected

**Additional normative rules for this requirement:**

Template files MUST be relative paths that do not escape the station directory, and their loaded content is part of the station revision identity.
Template rendering supports `joborder.params`, an arbitrary mapping resolved through the station context-reference system before rendering.
The template context exposes run, task, station, order-window, input, output, manifest, working-root, and parameter values, and the renderer provides helper functions including `input "FILE_TYPE"` (exactly one resolved input path), `inputs "FILE_TYPE"` (all resolved paths for a file type), `param "name"`, the quoting functions `yamlq`, `tomlq`, and `jsonq`, and the path functions `joinPath`, `basename`, and `dirname`.
Rendered template output MUST parse as the declared `joborder.format`; generation MUST fail before dispatch if template execution or format validation fails.
`joborder.include` is only valid with the default renderer; template renderer stations MUST use `joborder.params` instead.

### Requirement: Default renderer document content

The default renderer's generated job order MUST include, directly or by reference: `veriproc_meta` unless the station explicitly declares `joborder.meta: false` — containing job order schema version, task identity, retry index, run reference, station identity, station revision identity, generator identity, and manifest reference when present; the processing window; a facility block when declared through `joborder.include` or generator policy

#### Scenario: Meta omission
- **GIVEN** a default-renderer station declaring `joborder.meta: false`
- **WHEN** the job order is generated
- **THEN** `veriproc_meta` is absent from the document while internal audit records, manifest export, and the joborder artifact record remain

**Additional normative rules for this requirement:**

root-level `log_level` when declared through `joborder.include` or generator policy; root-level `dyn_params` containing algorithm-facing variable parameters when declared through `joborder.include` or generator policy; resolved input references pointing into the working-root input namespace (one entry may reference one or several filesystem objects for the same `file_type`); expected output declarations
and an optional reference to the persisted input manifest.

`joborder.meta: false` is valid only for the default renderer and omits the `veriproc_meta` mapping for processors that reject unknown top-level groups; it MUST NOT remove the internal audit records, manifest export, joborder artifact record, run record, or station revision reference maintained by VeriProc.

A `generators.joborder` entry in the instance configuration records a generator identity label only (external generator-module execution is not implemented); the recorded identity MUST still appear in run metadata when audit-relevant or fingerprint-relevant.

### Requirement: Joborder include merging and reserved names

The station `joborder.include` field is an arbitrary YAML subtree resolved through the station context-reference system before rendering and then merged into the generated joborder document.
Generated fields are reserved and MUST NOT be overridden by `joborder.include`: `veriproc_meta`, `order`, `inputs`, and `outputs`.

#### Scenario: Reserved top-level key rejected
- **GIVEN** a `joborder.include` containing a top-level `task_id` key
- **WHEN** run preparation runs
- **THEN** preparation fails with an error identifying the reserved name

#### Scenario: Deterministic deep merge
- **GIVEN** a `joborder.include` with a non-reserved nested mapping
- **WHEN** the joborder is rendered
- **THEN** the mapping is deep-merged deterministically with existing non-reserved content

**Additional normative rules for this requirement:**

Legacy top-level VeriProc metadata names are also reserved to prevent reintroducing system-owned fields outside `veriproc_meta`: `schema_version`, `joborder_id`, `task_id`, `retry_index`, `run_ref`, `station`, `generator`, and `manifest`.
If `joborder.include` contains any reserved top-level key or attempts to override a reserved nested generated field, preparation MUST fail.
Non-reserved mappings are deep-merged deterministically; non-reserved scalar and sequence values are inserted as provided after context resolution.
The fields `facility`, `dyn_params`, `temporary_folder`, `config_files`, `log_level`, and station-specific algorithm fields are intended to be supplied through `joborder.include` when needed.

### Requirement: Flat joborder inputs list
The default job-order model MUST keep `inputs` as one flat list keyed by unique `file_type` values; inputs MUST NOT be split by category unless a station-specific schema explicitly requires it. Within that list, each input entry represents one declared station input and may bind to one or several filesystem objects. The default schema MUST use a `files` field for each entry even when only one object is present; the field name does not restrict entries to regular files.

#### Scenario: Single object still uses files field
- **GIVEN** a station input resolving to exactly one filesystem object
- **WHEN** the default joborder is generated
- **THEN** the entry contains a `files` list with one element

### Requirement: Working-root layout

The working root MUST contain: the configured joborder file when `joborder.format` is `yaml`, `toml`, or `json`; `input/`; `output/`; `temp/`; `logs/` containing independent standard-output and standard-error logs named `run_out.log` and `run_err.log`; `manifest/` containing the exported resolved input manifest used for audit or replay

#### Scenario: Working root contents
- **GIVEN** a prepared run with `joborder.format: yaml`
- **WHEN** the working root is inspected
- **THEN** it contains `joborder.yaml`, `input/`, `output/`, `temp/`, `logs/`, and `manifest/`

#### Scenario: Alternate task-out location
- **GIVEN** an algorithm writing `output/task-out.yaml` instead of a root-level `task-out.yaml`
- **WHEN** run finalization reads task-out metadata
- **THEN** the alternate location is accepted

**Additional normative rules for this requirement:**

and an optional `task-out.yaml` at the working-root root, with `output/task-out.yaml` additionally accepted as an alternate location.
The backend MUST treat the working root as write-once control context plus mutable run outputs; if partial setup fails, the run MUST be recoverable or safely failed according to the recovery rules.

### Requirement: Working-root path is not run identity

The working-root path is not the run identity; the canonical run key is `(task_id, retry_index)`.
Human-readable path components are operational presentation values derived from validated metadata and MUST NOT become foreign keys, provenance targets, API identities, or deduplication owners.

#### Scenario: Path not used as provenance target
- **GIVEN** a run with working-root path `.../station-a/task-x/r5`
- **WHEN** provenance or deduplication records reference the run
- **THEN** they reference the canonical `(task_id, retry_index)` identity, not the path

**Additional normative rules for this requirement:**

The working-root generator MAY use station ID, task start and end times, retry index, run creation or preparation timestamp, and another deterministic per-run uniqueness component, and MUST NOT require the preferred display form `task_id/rN` to be visible in the path.
Instance-level `generators.working_root` entries record an identity label only; external generator-module execution is not implemented.

### Requirement: Runtime environment contract

The execution adapter MUST inject a predefined runtime environment into the job process, including at least `VERIPROC_WORKING_ROOT`, `VERIPROC_STATION_ID`, `VERIPROC_TASK_ID`, `VERIPROC_RETRY_INDEX`, `VERIPROC_RUN_REF`, `VERIPROC_WINDOW_START`, and `VERIPROC_WINDOW_END`; when a joborder file is generated, `VERIPROC_JOBORDER_PATH` MUST also be included.

#### Scenario: Runtime environment injection
- **GIVEN** a dispatched run with a generated joborder
- **WHEN** the job process starts
- **THEN** the environment contains all baseline `VERIPROC_*` variables plus `VERIPROC_JOBORDER_PATH`, and operator-defined `execution_env` variables without overriding any `VERIPROC_` variable

#### Scenario: No shell interpretation
- **GIVEN** `execution.args` containing a resolved value with shell metacharacters
- **WHEN** the process is launched
- **THEN** the value is passed as a single literal string argument without shell parsing

**Additional normative rules for this requirement:**

When the implementation has a VeriProc job record identifier before launch, it MAY include `VERIPROC_JOB_ID`; scheduler-native identifiers are separate and MUST NOT be required for station command resolution before scheduler admission.
`VERIPROC_RUN_REF` SHOULD use the preferred display convention (for example `scen2-20250703T111239000-a7sf7f/r5`) as a convenience representation of the canonical composite.
Additional deployment-specific variables may be provided but MUST NOT replace the job order as the primary execution contract when a joborder file is enabled.
These variables are not serialized into the job order by default and correspond to station context references such as `<working_root>`, `<station_id>`, `<task_id>`, `<retry_index>`, `<run_ref>`, `<start>`, `<end>`, and `<joborder>` when applicable.

Station execution SHOULD declare command-line arguments through `execution.executable` and `execution.args`, where each argument is resolved before launch without shell interpretation and passed as strings after context resolution. Implementations MAY offer an explicitly documented shell mode, but the normative execution model avoids shell parsing and therefore shell quoting ambiguity.

### Requirement: Run lifecycle states

The implementation MUST define an internal run state machine with the states `pending` (task accepted, run not yet materialized for dispatch), `preparing` (working root, manifests, and job order being created), `ready` (preparation finished, queued for scheduler submission), `dispatched` (scheduler accepted the job, waiting for runtime start), `running` (job executing), `finalizing` (scheduler finished, backend validating outputs and routing)

#### Scenario: Terminal state stability
- **GIVEN** a run in state `complete`
- **WHEN** any lifecycle pass observes the run
- **THEN** the state remains `complete`

#### Scenario: Idempotent re-entry of preparing
- **GIVEN** a run interrupted in state `preparing`
- **WHEN** the backend restarts and reconciles
- **THEN** the run re-enters `preparing` safely, reconciling filesystem and DB, and continues or fails deterministically

**Additional normative rules for this requirement:**

`complete` (outputs validated, canonical completion decision recorded), `failed` (execution or validation failed irrecoverably), and `cancelled` (operator or system cancelled).
The states are normative unless an implementation documents an equivalent model with the same recovery semantics.
`pending`, `preparing`, `ready`, `dispatched`, `running`, and `finalizing` MUST be idempotent to re-enter; `complete`, `failed`, and `cancelled` are stable terminal states, with `failed` terminable only by an operator-triggered retry creating a new run.
The API MAY expose a simplified subset, but the backend MUST preserve enough internal distinction to recover correctly.

Implementation status: there is no separate `dispatching` or `orphaned` state in the current implementation; submission uncertainty between scheduler acceptance and job-record persistence is covered by stale-observation reconciliation for `dispatched`/`running` runs, not by a dedicated run state.

### Requirement: Asynchronous recovery expectations

The rules in this requirement are normative and MUST be satisfied by the implementation.

#### Scenario: Recovery re-enters finalizing
- **GIVEN** a crash after scheduler completion but before output validation
- **WHEN** the backend restarts
- **THEN** the run re-enters `finalizing` and output validation runs idempotently

**Additional normative rules for this requirement:**

The backend recovery rules per non-terminal state are: `pending` resumes setup or marks the run failed if prerequisites are missing; `preparing` reconciles filesystem and database and continues or fails deterministically; `ready` runs are resubmitted by the dispatcher (subject to the known submission-uncertainty limitation); `dispatched` and `running` runs are polled and advanced when runtime evidence appears, preserving logs and status
and `finalizing` runs re-run validation and downstream reconciliation safely.

### Requirement: Scheduler adapter operations
The scheduler adapter MUST support at least: submit job; query job by scheduler identifier; list or reconcile jobs associated with a run when possible; and cancel job when supported. The backend MUST NOT treat submission as complete until it persists the scheduler identifier.

#### Scenario: Submission completion
- **GIVEN** a scheduler submission that was accepted
- **WHEN** the scheduler identifier has not yet been persisted
- **THEN** the submission is not treated as complete

**Additional normative rules for this requirement:**

Implementation status: if the scheduler accepts a job but the backend crashes before the job ID is persisted, the run remains in `ready` and will be resubmitted by the dispatcher on restart; without a submission-uncertainty marker or scheduler-side reconciliation keyed by a persistent run identity, a crash in this window can result in a duplicate submission (Known Issue). The reconciler separately handles stale observations for `dispatched`/`running` runs whose job records exist.

### Requirement: SLURM executor modes

The rules in this requirement are normative and MUST be satisfied by the implementation.
Two distributed SLURM modes are defined: `slurm-native` submits a wrapper with `sbatch` and on the compute node executes the resolved station executable and arguments directly with the VeriProc runtime environment injected

#### Scenario: Slurm-docker execution
- **GIVEN** a `slurm-docker` station run
- **WHEN** the wrapper executes on the compute node
- **THEN** the station executable runs inside the declared Docker image with the configured mounts, user mapping, and runtime environment

**Additional normative rules for this requirement:**

`slurm-docker` submits a wrapper with `sbatch` and on the compute node runs `docker run --rm` with the configured image, bind mounts, user mapping, and runtime environment, then executes the resolved station executable and arguments inside the container.
The names `plain-slurm` and `docker-slurm` are understandable operator language, but `slurm-native` and `slurm-docker` are preferred.

An implementation MAY initially support single-run SLURM submissions only; array submissions and node-packed `srun` batching are optimizations and MUST NOT be required for correctness. If array or packed submissions are added, each VeriProc run MUST still have an independent job record or an unambiguous scheduler-step reference, independent working-root logs, independent exit-code evidence, and independent cancellation/reconciliation behavior.

### Requirement: SLURM submission construction

The SLURM adapter MUST build submissions from the frozen run context, effective station execution configuration, and merged executor settings.
It MUST map effective resources to `sbatch` options for job name, account when configured, partition, QoS when configured, CPUs per task, memory, walltime, output and error handling, exported environment, and any explicitly allowed extra scheduler arguments.

#### Scenario: Parsable submission
- **GIVEN** a SLURM submission
- **WHEN** the job is admitted
- **THEN** the job ID is obtained via `--parsable` and persisted immediately

#### Scenario: Unsafe sbatch option rejected
- **GIVEN** station `slurm` overrides containing an unsupported or unsafe `sbatch` option
- **WHEN** configuration validation or dispatch preparation runs
- **THEN** the option is rejected

**Additional normative rules for this requirement:**

Backend-managed defaults come from `instance.yaml`; station-level `execution.resources` and `execution.slurm` override those defaults for that station only.
Unsupported or unsafe `sbatch` options MUST be rejected during configuration validation or dispatch preparation.
SLURM submission MUST use `sbatch --parsable` or an equivalent machine-readable form so the scheduler identifier can be persisted immediately after admission.
The job record's executor type MUST be the canonical adapter mode (such as `slurm-native` or `slurm-docker`), and the scheduler identifier MUST be the SLURM job ID or documented job-step identifier returned by the scheduler.
Submission uncertainty MUST be recorded before any retry decision.

### Requirement: SLURM wrapper contract

The SLURM wrapper MUST create or verify the working-root subdirectories it owns, including `logs/` and `output/`, without deleting prepared inputs, manifests, joborders, or user-visible audit files.

#### Scenario: Exit-code marker on failure
- **GIVEN** a station process that fails with a non-zero exit
- **WHEN** the wrapper finishes
- **THEN** the exit code is recorded in the `.exit_code` marker

#### Scenario: Prepared files preserved
- **GIVEN** a wrapper run on an allocated node
- **WHEN** working-root subdirectories are created or verified
- **THEN** prepared inputs, manifests, joborders, and user-visible audit files are not deleted

**Additional normative rules for this requirement:**

It MUST redirect the station process stdout to `logs/run_out.log` and stderr to `logs/run_err.log`, and SHOULD write scheduler-wrapper diagnostics to separate files such as `logs/exec-slurm-<scheduler-id>.out.log` and `logs/exec-slurm-<scheduler-id>.err.log` when available.
The wrapper MUST write the station process exit code to an exit-code marker under the working root (for example `.exit_code`) even when the process fails.
Signal handlers SHOULD translate scheduler termination signals into conventional process exit codes such as `143` for SIGTERM and `130` for SIGINT when the underlying process does not provide a clearer code.

For `slurm-docker`, the wrapper MUST generate a stable container name from the run or scheduler identity, pass the same VeriProc runtime environment into the container, bind every required shared root, run as the configured user mapping when provided, and stop the container on SIGTERM or SIGINT when Docker is still running. Docker image, mounts, and user mapping are configuration, not station command arguments, and MUST be recorded or reconstructible from the frozen station revision and effective executor settings.

### Requirement: SLURM polling state mapping

Polling SHOULD use `sacct`, `squeue`, or equivalent SLURM commands through the configured connection.
The adapter MUST map scheduler states to executor states without treating scheduler success as run completion.

#### Scenario: Scheduler completion with nonzero exit
- **GIVEN** a SLURM job in state `COMPLETED` with exit code `1:0`
- **WHEN** polling maps the state
- **THEN** the executor state is failed, not succeeded

#### Scenario: Exit-code marker cannot override timeout
- **GIVEN** a SLURM job with authoritative terminal state `TIMEOUT` and a wrapper exit-code marker of `0`
- **WHEN** reconciliation runs
- **THEN** the run is treated as failed per the scheduler's authoritative state

**Additional normative rules for this requirement:**

Typical mappings are: `PENDING`, `CONFIGURING`, and equivalent states to queued; `RUNNING` and completing states to running; `COMPLETED` with exit code `0:0` to succeeded; `FAILED`, non-zero exit code, `TIMEOUT`, `NODE_FAIL`, and other terminal failure states to failed; and `CANCELLED` to cancelled.
Native scheduler state, elapsed time, node list, and exit-code fields SHOULD be retained as diagnostic metadata when available.
If scheduler accounting is stale or unavailable, the wrapper exit-code marker MAY be used as reconciliation evidence, but it MUST NOT override an authoritative SLURM terminal failure such as timeout, cancellation, or node failure.

### Requirement: SLURM cancellation
Cancellation is supported only when the adapter is configured with a usable `scancel` command and policy permits it. A cancellation request MUST call `scancel` for the scheduler identifier when the job is cancellable, persist cancellation-request state, and rely on polling or reconciliation to observe terminal cancellation. If `scancel` is unavailable, the adapter MUST report cancellation unsupported rather than marking the run cancelled locally.

#### Scenario: Cancellation unsupported
- **GIVEN** an adapter without a usable `scancel` command
- **WHEN** a cancellation is requested
- **THEN** the adapter reports cancellation unsupported and the run is not marked cancelled locally

### Requirement: Output validation completion conditions
Scheduler success alone MUST NOT transition a run to `complete`. Completion requires: successful scheduler termination according to executor semantics; successful output validation for all mandatory outputs; successful persistence of artifact records; and successful downstream routing reconciliation, or an explicit policy allowing routing to complete asynchronously after `complete`.

#### Scenario: Scheduler success without outputs
- **GIVEN** a run whose scheduler job completed successfully but whose mandatory outputs are missing
- **WHEN** finalization runs
- **THEN** validation fails and the run does not transition to `complete`

**Additional normative rules for this requirement:**

Output validation SHOULD include more than existence when domain risk justifies it. The station definition MAY specify: minimum size for regular files or object-level size where meaningful; expected object kind when the station declares an output must be a regular file or directory; effective output `filename_pattern` compliance including `<FILE_TYPE>` substitution from the declared output definition; metadata inspection hooks; checksum generation only when required by station or deployment integrity policy; and domain-specific validators.

### Requirement: Artifact records and directory outputs

Artifacts MUST record logical type, filesystem path, producing run, size when available, checksum metadata when available under the integrity policy, validation outcome, and whether the path identifies a regular file or a directory.
Under the default `available_only` checksum policy, run completion MUST remain valid without system-computed checksums.

#### Scenario: Directory artifact validation
- **GIVEN** a mandatory directory output whose directory exists with the expected name
- **WHEN** output validation runs under baseline policy
- **THEN** validation succeeds without recursive child inspection or a recursive digest

**Additional normative rules for this requirement:**

Directory output validation in the baseline is object-level validation: existence, expected object kind, effective name or naming-rule compliance, and any explicit station policy.
Baseline completion MUST NOT depend on recursively inspecting every child entry in a directory output or computing a recursive directory checksum.

### Requirement: Rolling archive publication declaration
After mandatory output validation succeeds, the backend MAY publish selected outputs from `working_root/output/` into one or more configured rolling archives according to per-output publication configuration. Each output definition that declares a `publish` block is published independently; outputs without a `publish` block are not published. Publication MUST run after validation and before downstream tasks are created if downstream input discovery depends on the rolling archive location.

#### Scenario: No publish block
- **GIVEN** an output definition without a `publish` block
- **WHEN** the run finalizes
- **THEN** that output is not published to any rolling archive

### Requirement: Publication target and modes

Publication is declared per output definition using a `publish` block containing `rolling_archive`, `mode`, and an optional `target_subpath`.
The `rolling_archive` field references a named rolling archive from the instance configuration and MUST NOT be an arbitrary filesystem path.

#### Scenario: Default copy at archive root
- **GIVEN** an output with `publish: {rolling_archive: co2m_hot}` and no mode or subpath
- **WHEN** publication runs
- **THEN** the object is copied to the root of archive `co2m_hot` under its effective published name

#### Scenario: Target subpath escape rejected
- **GIVEN** a `target_subpath` of `../../elsewhere`
- **WHEN** publication is prepared
- **THEN** the subpath is rejected for escaping the selected archive root

**Additional normative rules for this requirement:**

By default the publication target path MUST place the published filesystem object at the root of the selected archive using its effective published name; VeriProc MUST NOT implicitly prepend `station_id` or any other derived directory segment.
If a deployment requires a directory below the archive root, it MUST be declared explicitly through `target_subpath`, which MUST be relative, normalized, and validated not to escape the selected archive root.
The publication `mode` MUST be explicit — for example `copy`, `move`, `hardlink`, or `symlink` — and MUST be allowed by deployment policy; when absent it defaults to `copy`.
Directory artifacts MAY be copied or moved as opaque directory trees, or symlinked at the directory root when policy allows; the baseline does not require recursive hardlink behavior for directories.

### Requirement: Multi-object output publication
If an output definition is declared as multi-object (`multiple: true`), every validated filesystem object matching the effective filename pattern or glob MUST be recorded as a separate output artifact and published independently. A single output definition MUST NOT collapse a fan-out set to only the newest or lexicographically last matching object.

#### Scenario: Multiple artifacts published independently
- **GIVEN** a `multiple: true` output with three matching validated objects
- **WHEN** publication runs
- **THEN** three separate artifact records are created and each object is published independently

### Requirement: Publication idempotency and metadata

Publication MUST be idempotent: retrying finalization MUST NOT create duplicate logical archive entries.
The backend MUST record publication metadata including source artifact, target rolling archive, target path, publication mode, object kind, size when available, checksum metadata when available under the integrity policy, and timestamp.

#### Scenario: Idempotent republication
- **GIVEN** a run finalized twice due to retry after partial failure
- **WHEN** publication runs the second time
- **THEN** no duplicate logical archive entries are created

#### Scenario: Async worker single-base limitation
- **GIVEN** a pending-publication record whose `ArchiveID` differs from the single configured archive base
- **WHEN** the asynchronous worker resolves it
- **THEN** the record is at risk of mispublication, as tracked in the Known Issue

**Additional normative rules for this requirement:**

Directory publication MUST NOT require per-child checksum verification before copy, move, or symlink publication succeeds.
Retention cleanup in a rolling archive MUST NOT delete the artifact record or the original working-root output unless a separate retention policy explicitly allows working-root cleanup.

Implementation status: the implementation contains two publication paths — a synchronous path inside run finalization that publishes validated outputs directly and records state `published`, and an asynchronous pending-publication worker for records not published synchronously. The asynchronous worker currently resolves every pending record against a single configured archive base rather than per-record archive identity; records whose `ArchiveID` names a different archive are at risk of mispublication (tracked as a Known Issue).

### Requirement: Publication is not grouping authority

For grouped fan-in workflows, publication of sub-window or sub-granule outputs to a rolling archive is an operational storage concern, not the authoritative grouping mechanism.
If child outputs are published, the backend MUST still derive aggregation membership from the split group and canonical child-task selection, then link the selected artifacts into the aggregation run's `working_root/input/`.

#### Scenario: Aggregation membership from split group
- **GIVEN** published child outputs in a rolling archive and a completed split group
- **WHEN** the aggregation input set is derived
- **THEN** membership comes from the split group and canonical child-task selection, with selected artifacts linked into the aggregation run's `working_root/input/`

**Additional normative rules for this requirement:**

A deployment MAY define a dedicated rolling archive for sub-window or sub-granule products when separate retention, visibility, or reuse policy is needed; mixing such outputs into a general-purpose archive is permitted only if archive scanning is not the sole mechanism for aggregation membership selection.

### Requirement: Task-out routing and validation

Downstream routing MAY be driven by an algorithm-produced `task-out.yaml` or by the station revision's default `downstream` list.
Before creating downstream tasks, the backend MUST validate `task-out.yaml` for: schema validity; target station existence; timestamp validity; duplicate child task entries; and split-group declarations when required.

#### Scenario: Malformed task-out
- **GIVEN** a `task-out.yaml` with an invalid schema
- **WHEN** downstream routing is evaluated
- **THEN** no downstream tasks are created and the run moves to `failed` unless fallback routing is explicitly permitted

**Additional normative rules for this requirement:**

Malformed `task-out.yaml` MUST NOT produce partial downstream creation; the run SHOULD move to `failed` unless station policy explicitly permits fallback routing.

### Requirement: Split-group representation
Fractional processing MUST be coordinated by an explicit split group (or equivalent concept). A split group MUST represent: the parent task or parent run; the parent processing window; the intended downstream station or stage; the expected child set or the rule by which completion of the child set is determined; and closure, readiness, and grouped-evaluation state.

#### Scenario: Composite state progression
- **GIVEN** a split group with children still arriving
- **WHEN** the group state is observed
- **THEN** it is `open` until closure, then evaluates readiness and transitions to `ready` or `failed`

#### Scenario: No aggregation before readiness
- **GIVEN** a split group whose readiness policy has not evaluated true
- **WHEN** downstream processing is considered
- **THEN** no aggregation run is triggered

**Additional normative rules for this requirement:**

Implementations MAY expose a composite group state derived from closure, readiness, and grouped-evaluation state. If exposed, the composite state SHOULD include at least `open`, `ready`, `aggregating`, `complete`, and `failed`, or a documented subset with equivalent semantics: `open` means membership is not closed or is still accepting children; `ready` means the group is closed and readiness has evaluated true; `aggregating` means aggregation or terminal-member evaluation is in progress; `complete` means readiness has been recorded or the aggregation stage completed according to policy; `failed` means readiness or grouped evaluation failed according to policy.

The system MUST NOT trigger an aggregation run until the split group's readiness policy evaluates true. Implementations MAY represent that transition as `ready` or, for completion-only readiness evaluation, directly as `complete`. Readiness MUST be based on explicit completion rules, not inferred only from task history strings.

### Requirement: Fan-in policy definition
The station or system policy MUST define: whether all child tasks are known up front or may be discovered dynamically; whether missing children cause indefinite wait, bounded timeout, or terminal failure; whether partial aggregation is allowed; how duplicate child completions are ignored or reconciled; and how retries of child runs affect group readiness and canonical aggregation.

#### Scenario: Partial aggregation
- **GIVEN** a split group where some terminal members failed and at least one has a canonical completed run
- **WHEN** all registered members are terminal
- **THEN** the group is marked `complete` and the aggregation task is created from the canonical contributions

#### Scenario: All members failed
- **GIVEN** a split group where every terminal member failed to produce a canonical contribution
- **WHEN** the final member becomes terminal
- **THEN** the group is marked `failed`

**Additional normative rules for this requirement:**

The default bounded partial-aggregation policy is: wait until the expected child count has registered; wait until all registered members are terminal; mark the group `complete` and create the aggregation task when at least one member has a canonical completed run; and mark the group `failed` only when all terminal members failed to produce a canonical contribution. Terminal failed members MUST still trigger readiness re-evaluation so the group cannot remain stuck after the final failure.

### Requirement: Aggregation task creation

For split groups using aggregation semantics, the backend MUST NOT create the aggregation task when the parent splitter emits child tasks.
It MUST persist the split group, track child-task completion for that `group_id`, and create the aggregation task only after the group is closed and the readiness policy evaluates to true.
The aggregation task MUST reference exactly one split group.

#### Scenario: Aggregation input materialization
- **GIVEN** a closed split group with readiness satisfied and canonical published artifacts for contributing children
- **WHEN** the aggregation run is prepared
- **THEN** its manifest, job-order input references, and `working_root/input/` symlinks reference the canonical published artifacts selected for that group

**Additional normative rules for this requirement:**

Its processing window MUST default to the parent window of that split group unless station policy defines another grouped-evaluation window, which MUST be recorded on the split group as a grouped-evaluation window override.

The backend MUST determine the aggregation input set from the split-group membership and the canonical completed result of each contributing child task, excluding failed or missing children only when the readiness policy permits partial aggregation. It MUST then materialize that input set into the aggregation run's working root using the same mechanism as any other run: resolved input manifest entries, job-order input references, and symlinks under `working_root/input/`. If validated output publication to a rolling archive is required before downstream consumption, the aggregation input set MUST be derived from the canonical published artifacts selected for that split group; archive location MUST NOT replace split-group membership as the authoritative routing mechanism.

### Requirement: Processing fingerprint composition

Deduplication MUST be based on a processing fingerprint, not only on station and time window.

#### Scenario: Fingerprint includes station revision
- **GIVEN** two tasks with identical windows and inputs but different station revisions
- **WHEN** fingerprints are computed
- **THEN** the fingerprints differ

#### Scenario: Force flag in fingerprint
- **GIVEN** a forced rerun of otherwise-identical scientific context in the current implementation
- **WHEN** the fingerprint is computed
- **THEN** it is distinct from the canonical owner's fingerprint due to the included `force` flag

**Additional normative rules for this requirement:**

The fingerprint MUST include all context that can materially affect scientific outputs, including at least: station identity; station revision identity; task processing window; resolved product identities; resolved auxiliary identities that affect outputs; algorithm image or executable version; custom working-root or job-order generator identity when output-affecting; relevant runtime parameters; facility mode if it affects outputs
rolling archive source identities when archive choice can affect reproducibility; and job order schema version when schema meaning affects execution.
An implementation MAY include additional fields such as checksums, external calibration versions, or deployment profile identifiers.

Implementation status: the current implementation additionally includes the task's `force` flag in the processing fingerprint, so a forced rerun of otherwise-identical context always produces a distinct fingerprint and never collides with the canonical owner; this weakens the fingerprint-as-scientific-context invariant and is tracked as a Known Issue.

### Requirement: Canonical and non-canonical runs
If a completed run already owns the same processing fingerprint, the backend MUST NOT dispatch a new canonical run unless explicitly overridden. A request with `force=true` MAY create a new run, but that run MUST be recorded as a non-canonical rerun unless an explicit promotion or replacement action occurs. Forced reruns MUST NOT silently overwrite the previous canonical record. The API MUST let users distinguish canonical completion from superseded or forced reruns.

#### Scenario: Duplicate fingerprint not dispatched
- **GIVEN** a completed run owning fingerprint F
- **WHEN** a new task resolves to fingerprint F without force
- **THEN** no new canonical run is dispatched

#### Scenario: Forced rerun non-canonical
- **GIVEN** a completed canonical run owning fingerprint F
- **WHEN** a task with `force=true` runs to completion
- **THEN** the new run is recorded as non-canonical and the previous canonical record is not silently overwritten

### Requirement: Mandatory crash-recovery cases
The backend MUST define recovery behavior for failure gaps between every external side effect. The mandatory cases and their implementation status are:

#### Scenario: Crash before task commit
- **GIVEN** a client submission that crashed before the task was committed
- **WHEN** the client retries with the same idempotency key
- **THEN** the retry is safe and produces the same logical result

#### Scenario: Crash in submission window
- **GIVEN** a crash after the scheduler accepted a job but before the job ID was persisted
- **WHEN** the backend restarts
- **THEN** the current implementation resubmits the `ready` run unconditionally, which may duplicate a submission whose outcome is unknown (Known Issue)

#### Scenario: Unowned working root
- **GIVEN** a crash after working root creation but before run record commit
- **WHEN** the backend restarts
- **THEN** the current implementation leaves the unowned working root in place and the cleaner only purges DB-recorded entities (Known Issue; deterministic classification or reclamation is not implemented)

**Additional normative rules for this requirement:**

- Crash before task DB commit: client retry MUST be safe through submission idempotency. (Implemented)
- Crash after task DB commit but before run preparation completes: restart MUST resume or safely fail the partial run without duplicate canonical work. (Implemented)
- Crash after working root creation but before run record commit: startup reconciliation SHOULD detect unowned working roots and classify or reclaim them deterministically. (Not implemented — Known Issue: unowned working roots are left in place; the cleaner only purges entities recorded in the DB)
- Crash after scheduler submission request but before job ID persistence: restart SHOULD reconcile with the scheduler before any resubmission decision. (Partially implemented — Known Issue: a run in `ready` is resubmitted unconditionally, which can duplicate a submission whose outcome is unknown)
- Crash after scheduler completion but before output validation: restart MUST re-enter `finalizing` and re-run validation idempotently. (Implemented)
- Crash after downstream tasks are created but before completion is marked: restart MUST reconcile child-task creation idempotently and avoid duplicates. (Implemented)

### Requirement: Idempotency and uniqueness
The following operations MUST be idempotent or protected by uniqueness constraints: task submission; run creation for a given retry request; downstream task creation from a completed run; split-group creation and readiness transitions; artifact recording for a finalized run; rolling archive publication for a finalized run; and deduplication record creation.

#### Scenario: Idempotent downstream creation
- **GIVEN** a completed run whose downstream task creation is re-executed after a restart
- **WHEN** reconciliation runs
- **THEN** no duplicate downstream tasks are created

### Requirement: API query semantics
The API SHOULD expose at least: task submission with idempotency support; task and run query endpoints with filtering and pagination; access to logs and artifact metadata; canonical versus rerun status; and cancellation where supported. The API MUST define whether each response is strongly consistent or eventually consistent. At minimum, queries against the database MUST reflect committed control-plane state.

#### Scenario: Database query consistency
- **GIVEN** a task query served from the database
- **WHEN** the response is returned
- **THEN** it reflects committed control-plane state

### Requirement: Operational constraints

The rules in this requirement are normative and MUST be satisfied by the implementation.
The architecture assumes explicit operational policy for: working-root retention; rolling archive retention and cleanup scheduling; cleanup of temporary files and stale failed roots; artifact archival; disk usage monitoring; validation and deployment of custom generator modules; scheduler timeout policy; concurrency limits and back-pressure; and audit logs, health checks, and metrics.

#### Scenario: Retention cleanup preserves records
- **GIVEN** rolling archive retention cleanup that removes expired published files
- **WHEN** cleanup completes
- **THEN** the database artifact records are not deleted unless working-root cleanup is separately permitted by policy

**Additional normative rules for this requirement:**

These policies MAY be deployment-specific, but the control plane SHOULD expose enough metadata to implement them consistently.

### Requirement: Docker container entrypoint override

A station with `execution.mode: slurm-docker` MAY declare
`execution.container.entrypoint` as a non-empty string (a single entrypoint
token, per the `docker run --entrypoint` contract). When set, the executor
MUST pass it to the container runtime as the entrypoint override so that the
station `executable` and args are interpreted as the container command
instead of being passed to the image's built-in ENTRYPOINT. When unset,
behavior MUST remain exactly as today (image entrypoint applies).

#### Scenario: Wrapper script in a processor-entrypoint image
- **GIVEN** a station image whose ENTRYPOINT is the processor binary and
  `execution.container.entrypoint` set to a shell
- **WHEN** the run is dispatched
- **THEN** the station executable script executes inside the container instead
  of being passed as an argument to the processor binary

#### Scenario: Default unchanged
- **GIVEN** a station without `entrypoint`
- **WHEN** the run is dispatched
- **THEN** the generated container command is identical to the current output
