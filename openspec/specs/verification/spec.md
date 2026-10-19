# Verification and Conformance Specification

## Purpose

Defines how VeriProc implementations demonstrate conformance to the specification: verification scope, the conformance model and profiles, the required test taxonomy and invariants, minimum MVP compliance scenarios, API/persistence/recovery/CLI compliance requirements, and the conformance evidence that must accompany a conformance claim. Passing happy-path workflow tests is not sufficient: a conforming implementation must preserve invariants, handle failure and recovery safely, expose the required API and CLI behavior, and produce auditable evidence. Derived from the former verification-and-conformance specification chapter (source document removed; see git history; §7.1–§7.10 normative; §7.11 informative and excluded).

## Requirements

### Requirement: Conformance Scope and Evidence Basis

Conformance claims MUST be grounded in demonstrable evidence covering control-plane semantics, persistence, API, executor-adapter, recovery, idempotency, provenance, canonicality, split-group (when supported), rolling archive (when supported), and CLI behavior (when provided). A claim MUST NOT rest only on successful task submission and scheduler execution.

#### Scenario: Happy-path-only claim rejected
- **GIVEN** an implementation whose test suite only demonstrates successful submission and scheduler success
- **WHEN** a conformance claim is made
- **THEN** the claim is invalid because identity, recovery, canonicality, and provenance behavior was not verified

#### Scenario: Evidence content
- **GIVEN** an implementation claiming conformance
- **WHEN** the claim is published
- **THEN** it includes automated test results, a scenario matrix, tested configurations, declared deviations, and explicitly deferred features
### Requirement: Specification Conformance Model

Conformance is defined at the level of implemented behavior, not source organization, language, packaging, or test framework. An implementation MUST NOT claim support for a feature unless both the required behavior is implemented and the required verification has passed in the claimed environment. Unimplemented or unverified features MUST be declared unsupported, deferred, experimental, or non-conformant.

#### Scenario: Unverified feature declaration
- **GIVEN** a feature that is implemented but lacks passing verification in the claimed environment
- **WHEN** the conformance claim is assembled
- **THEN** the feature is declared non-conformant or deferred rather than silently omitted
### Requirement: MVP Conformance Profiles

A conformance declaration MUST identify the executor configuration tested. Declared profiles include: core MVP with stub executor, `slurm-native`, `slurm-docker`, MVP with rolling archive publication, and MVP with split-group aggregation. Core MVP conformance may defer optional behavior only if the deferral is visible in the conformance report and the features are not exposed as supported.

#### Scenario: Stub executor profile claim
- **GIVEN** the stub executor profile declared
- **WHEN** verification runs
- **THEN** intake, persistence, API behavior, manifest and job-order preparation, canonicality, idempotency, finalization, recovery, and CLI behavior pass with the deterministic stub executor

#### Scenario: Visible deferral
- **GIVEN** an implementation deferring split-group aggregation while claiming core MVP
- **WHEN** the conformance report is produced
- **THEN** the deferral is listed and the feature is not exposed through API, CLI, or defaults as supported
### Requirement: Optional, Deferred, and Deviation Handling

Optional features follow the same rule as mandatory ones: they MUST NOT be claimed supported without both behavior and verification. Deferred features MUST be listed in the conformance evidence. Known deviations MUST be documented with affected requirement, observed behavior, risk, mitigation, and planned resolution or accepted limitation.

#### Scenario: Deviation documentation
- **GIVEN** a known deviation from the specification
- **WHEN** the conformance report is written
- **THEN** the deviation identifies the requirement, observed behavior, risk, mitigation, and planned resolution, and is not hidden
### Requirement: Test Taxonomy Coverage

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Unit tests insufficient
- **GIVEN** recovery behavior verified only by unit tests
- **WHEN** a conformance claim is assessed
- **THEN** recovery is not credited because crash-recovery tests with failure injection were not exercised

**Additional normative rules for this requirement:**

A conforming implementation MUST use a structured verification suite covering, at minimum: unit tests; database and migration tests; API contract tests; integration tests; end-to-end workflow tests; crash-recovery and reconciliation tests with failure injection; executor-adapter contract tests; CLI conformance tests; and backward-compatibility tests. Unit tests MUST NOT be the only evidence for behavior depending on persistence, filesystem side effects, scheduler interaction, API compatibility, or recovery.
### Requirement: Database and Migration Tests

Database tests MUST verify schema constraints, required record classes, referential integrity, immutability enforcement, uniqueness constraints, transactional behavior, projection rebuildability, and migration correctness. Migration tests MUST demonstrate that migrated runs continue to reference the same task, station revision, working root, manifest, artifacts, jobs, processing fingerprint, and canonicality records according to pre-migration semantics.

#### Scenario: Migration preserves meaning
- **GIVEN** a run persisted under an older schema version
- **WHEN** the migration runs
- **THEN** the migrated run references the same task, station revision, working root, manifest, artifacts, jobs, fingerprint, and canonicality records as before, verified by Go store migration tests
### Requirement: API Contract Tests

API contract tests MUST verify request and response shapes, required fields, version signaling, error response structure with machine-readable server error codes, pagination and deterministic ordering, consistency semantics, canonical versus non-canonical visibility, artifact and log access behavior, and tolerance for additive optional fields, covering both successful and failing requests.

#### Scenario: Error contract preserved
- **GIVEN** a failing API request
- **WHEN** the response is returned
- **THEN** it contains the machine-readable error code from the API error model, verified by HTTP API contract tests
### Requirement: End-to-End Workflow Tests

End-to-end tests MUST verify complete operator-visible workflows from submission through preparation, dispatch, result observation, finalization, artifact recording, canonicality decision, downstream routing where applicable, and API or CLI query. Scheduler success alone MUST NOT be treated as test success; output validation, artifact persistence, canonical classification, and API visibility must be verified.

#### Scenario: Full workflow assertion
- **GIVEN** a task submitted through the API
- **WHEN** the run completes end to end
- **THEN** the test asserts output validation results, persisted artifact metadata, canonical classification, and API visibility, not merely executor success
### Requirement: Executor-Adapter Contract Tests

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: SLURM native adapter verification
- **GIVEN** a claim of `slurm-native` conformance
- **WHEN** executor-adapter tests run
- **THEN** wrapper generation, effective `sbatch` arguments, runtime environment injection, independent logs, exit-code marker handling, scheduler state mapping, and configured cancellation pass against SLURM-specific scheduler responses, mapped to Go tests in `internal/executor/slurm_test.go`

#### Scenario: Stub claim not SLURM
- **GIVEN** conformance verified only with the stub executor
- **WHEN** the conformance report is produced
- **THEN** it does not claim SLURM adapter conformance

**Additional normative rules for this requirement:**

Executor-adapter tests MUST verify submission, query, cancellation where supported, scheduler identifier persistence, scheduler-native state observation, uncertainty handling, and reconciliation. A stub executor claim MUST NOT be represented as SLURM adapter conformance. `slurm-native` conformance MUST verify wrapper generation, effective `sbatch` arguments, runtime environment injection, independent stdout/stderr logs, exit-code marker handling, scheduler state mapping, and cancellation via `scancel` when configured; `slurm-docker` conformance additionally MUST verify Docker command construction, station-declared image use, bind mounts, user mapping, environment propagation, container cleanup on termination signals, and failure when a station omits the required image.
### Requirement: Identity and Immutability Invariants

Tests MUST verify that task routing content (destination `station_id`, processing window, force flag, parent references, split-group association from authorized backend workflows, client metadata) is immutable after task creation, and that task, run `(task_id, retry_index)`, job, station revision, artifact, split-group, and processing fingerprint identities are distinct and not reused. Retrying a task MUST create a new run key without mutating an existing run.

#### Scenario: Routing content immutable
- **GIVEN** a created task
- **WHEN** later run lifecycle updates occur
- **THEN** routing content is unchanged, verified by store immutability tests

#### Scenario: Retry creates new run key
- **GIVEN** an existing run for a task
- **WHEN** the task is retried
- **THEN** a new `(task_id, retry_index)` key is created and the prior run identity is untouched
### Requirement: Station Identity and Uniqueness

Station identity MUST be represented by `station_id`; `station_name` is operator-readable only. Active `station_id` and `station_name` values MUST each be unique within the deployment scope.

#### Scenario: Duplicate active station name rejected
- **GIVEN** an active station with a given `station_name`
- **WHEN** another active station is created with the same name
- **THEN** the creation is rejected, verified by station store tests
### Requirement: Opaque Identifier Invariants

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Prefix and timestamp policy
- **GIVEN** task creation under `naming.task_id_timestamp` set to `creation` or `start`
- **WHEN** the task ID is generated
- **THEN** the prefix matches the destination `station_id` and the timestamp segment follows the policy, covered by store naming tests

#### Scenario: Bare station rejected
- **GIVEN** a request using a bare `station_id` where a task identity is required
- **WHEN** the request is processed
- **THEN** it is rejected

**Additional normative rules for this requirement:**

Canonical identifiers MUST be opaque durable identifiers; task IDs, job IDs, artifact IDs, and split-group IDs MUST NOT encode parent run references, ordinals, provenance hints, retry reasons, scheduler IDs, or working-root paths. Task IDs MAY carry a matching `station_id` prefix and a timestamp segment per the `naming.task_id_timestamp` policy (default `creation`); the full task identifier MUST remain globally unique and a bare `station_id` MUST NOT be accepted as a task identity. Clients and backend code MUST use explicit API fields, not parse task IDs for authoritative window state. The run display form MUST be derived from `task_id` and `retry_index`.
### Requirement: Working Root Uniqueness

Each run MUST have a unique working-root path; retries MUST NOT reuse or overwrite a prior run's working root. Workable paths are generated by expanding `naming.working_root.path_template` under the configured shared-storage root, covering nested (`{station}/{task}/{run}`) and flat (`{task}-{run}`) layouts without a custom generator.

#### Scenario: Distinct working roots per retry
- **GIVEN** two runs of the same task
- **WHEN** their working roots are assigned
- **THEN** the paths are distinct
### Requirement: Working Root Template Invariants

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Path escape rejected
- **GIVEN** a `path_template` whose expansion escapes `working_root_base` or uses an unsupported placeholder
- **WHEN** the working root is computed
- **THEN** the run preparation fails

#### Scenario: Collision suffix behavior
- **GIVEN** an expanded readable path that already exists for another run
- **WHEN** the working root is assigned
- **THEN** the `collision_suffix` is appended to the final component only and the underlying run identity is unchanged

**Additional normative rules for this requirement:**

Working-root expansion MUST be deterministic for the same validated run context, unique once assigned, contained under `working_root_base`, and must correctly expand `{station}`, `{task}`, and `{run}` segment templates. Tests MUST reject unsupported placeholders, absolute templates, expansion escaping `working_root_base`, empty normalized components, and `.`/`..` semantics. On readable-path collision, the configured `collision_suffix` MUST be appended only to the final expanded component, only when a collision exists, without reordering readable components or acting as a second run identifier.
### Requirement: Frozen Execution Context

The resolved input manifest MUST be persisted and frozen before dispatch; once a run enters dispatching, its resolved input set MUST NOT change. Mandatory missing inputs MUST prevent dispatch; optional missing inputs MUST be recorded explicitly. The processing fingerprint MUST be computed after the execution context is frozen and before the run is dispatch-eligible, and MUST be immutable once computed.

#### Scenario: Manifest frozen before dispatch
- **GIVEN** a run in preparation
- **WHEN** dispatching begins
- **THEN** the frozen persisted manifest cannot be altered

#### Scenario: Fingerprint timing
- **GIVEN** a run with frozen manifest and execution context
- **WHEN** dispatch eligibility is evaluated
- **THEN** the processing fingerprint is already computed and persisted, verified by run preparation tests
### Requirement: Filename Pattern Parsing

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Pattern extraction
- **GIVEN** a file name matching an effective `filename_pattern` with global component definitions
- **WHEN** it is parsed
- **THEN** mission identifier, timestamps, and file type are extracted per positional rules, covered by classical matcher unit tests

**Additional normative rules for this requirement:**

Each input's effective `filename_pattern` MUST be determined from station-local override or instance-wide default, with `<FILE_TYPE>` substituted from the declared input/output definition, never inferred from discovery side effects. The instance configuration defines the global component rules for supported placeholders, enforced consistently across all station-local overrides. Parsing MUST extract mission identifier, start/end/generation timestamps, and parsed file type, using fixed positional extraction including wildcards such as `MISSION_ID = CDM?`, fixed-width extraction such as `FILE_TYPE = 16 ASCII characters`, and `YYYYMMDDTHHmmSSZ` timestamp parsing.
### Requirement: Interval Matching and Timestamp Parsing

Tests MUST verify the `overlaps`, `within_window`, and `covers_window` interval-matching policies, including declared time margins applied to the effective processing window before comparison.

#### Scenario: Margin-aware interval match
- **GIVEN** a candidate interval and a declared time margin
- **WHEN** the `within_window` policy is evaluated
- **THEN** the margin-adjusted effective window is used for the comparison

#### Scenario: Compact timestamp normalization
- **GIVEN** a submission with compact UTC millisecond timestamps
- **WHEN** the processing window is parsed
- **THEN** values normalize to UTC and malformed values are rejected before task creation

**Additional normative rules for this requirement:**

Task submission timestamps MUST be parsed from RFC 3339 whole-second and millisecond forms, RFC 3339 without explicit timezone (interpreted as UTC), compact `YYYYMMDDTHHmmSS` and `YYYYMMDDTHHmmSSmmm`, `YYYY-MM-DD` and `YYYYMMDD` (midnight UTC), with UTC normalization and rejection of malformed values.
### Requirement: Classical Matcher Invariants

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Winner tie-break order
- **GIVEN** multiple candidates in one logical interval group
- **WHEN** the winner is selected
- **THEN** generation time, discriminator, folder priority, and stable fallback are applied in order, verified by classical matcher unit tests

#### Scenario: Directory candidate symmetry
- **GIVEN** a directory entry matching the effective pattern
- **WHEN** classical matching runs
- **THEN** the directory participates in grouping and winner selection identically to regular files

**Additional normative rules for this requirement:**

The classical input matching algorithm MUST fully scan all configured folders, apply earliest-folder user priority only as a final tie-break, group candidates by declared input type plus parsed start and end timestamps, and select one winner per logical interval group. Winner selection MUST be deterministic: most recent real generation time, then lexicographically greatest post-generation discriminator, then highest-priority source folder, then the documented stable fallback; a dummy generation-time sentinel MUST rank below every real generation time. Regular-file and directory candidates MUST use the identical algorithmic shape subject only to declared `object_kind` filtering.
### Requirement: Integrity Policy and Directory Inputs

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: No mandatory checksums
- **GIVEN** the `available_only` integrity policy
- **WHEN** a run completes without system-computed checksums
- **THEN** completion remains valid

#### Scenario: Directory input materialization
- **GIVEN** a directory input matched by classical selection
- **WHEN** the working root is prepared
- **THEN** a symlink to the source directory root appears in `working_root/input/` and the manifest records object kind without recursive checksums

**Additional normative rules for this requirement:**

Under the default integrity policy `available_only`, input resolution and manifest freezing MUST record size, modification time, version metadata, and checksum metadata when available, but MUST NOT require full-file checksum computation. A resolved input MAY be a directory whose entry name matches the naming convention; directory inputs MUST be materialized into `working_root/input/` as symlinks to the selected source directory root, manifest entries MUST preserve object kind, and directory inputs MUST NOT require recursive checksum calculation.
### Requirement: Manifest Traceability

Manifest freezing MUST persist the effective `filename_pattern`, applied `window_match` policy, parsed filename metadata, logical interval grouping key, source folder priority, and winner comparison metadata needed to explain the selected candidate set. The working-root manifest export MUST carry the same traceability fields as persisted manifest entries.

#### Scenario: Working-root manifest parity
- **GIVEN** a frozen persisted manifest for a run
- **WHEN** the working-root manifest export is inspected
- **THEN** it carries the same pattern, policy, grouping, priority, and winner metadata as the persisted entries
### Requirement: Matching Decision Logging

Debug-level server logs emitted during input resolution MUST be sufficient to trace the classical matching decision for a run: declared input, effective pattern, resolved folder list or priorities, candidate parse and window-filter outcomes at debug level, grouping key, winner comparison keys, and selected source path, without full file reads or recursive directory inspection.

#### Scenario: Debug trace reconstruction
- **GIVEN** a run with debug-level logging enabled during resolution
- **WHEN** the log is reviewed
- **THEN** the matcher decision for each declared input can be reconstructed from pattern, folders, candidates, grouping, and winner keys
### Requirement: Context Reference Resolution

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Nested reference substitution
- **GIVEN** a station field containing `<facility.processing_center>`
- **WHEN** the context is resolved
- **THEN** the nested definition substitutes with mapping/list types preserved where applicable

#### Scenario: Missing reference rejection
- **GIVEN** a field referencing an undefined `<name.path>`
- **WHEN** the context is resolved
- **THEN** resolution fails rather than rendering the literal reference

**Additional normative rules for this requirement:**

Station context-reference resolution MUST cover instance `definitions` injected at the context root, nested references such as `<facility.processing_center>`, full-node substitution preserving mapping/list types, embedded scalar interpolation, rejection of missing references and reserved-name collisions, preservation of structured filename placeholders such as `<MISSION_ID>`, and freezing of resolved output-affecting values before dispatch. `instance_root` (absolute directory of the loaded instance config file) MUST be injected as a reserved runtime key available in all context-resolved fields including `preprocess_script` and `preprocess_args`.
### Requirement: Execution Environment and Arguments

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Reserved env prefix rejected
- **GIVEN** an instance `execution_env` variable named `VERIPROC_FOO`
- **WHEN** configuration is loaded
- **THEN** the configuration is rejected

#### Scenario: Unavailable joborder reference fails
- **GIVEN** `execution.args` referencing `<joborder>` at a station with joborder mode `none`
- **WHEN** dispatch is attempted
- **THEN** the run fails before script execution

**Additional normative rules for this requirement:**

Instance `execution_env` MUST load operator-defined variables, reject invalid names and the reserved `VERIPROC_` prefix, propagate into local and SLURM executor wrappers and Docker container environments for `slurm-docker`, and MUST NOT override VeriProc-defined runtime variables. Structured execution configuration MUST support `execution.executable` and `execution.args`, context-resolved values such as `<start>`, `<end>`, `<run_ref>`, and `<joborder>`, argument passing without shell interpretation by default, and MUST fail when an argument references a value unavailable for the station's declared joborder mode.
### Requirement: SLURM Config Merging

SLURM executor configuration merging MUST cover instance-level defaults (connection, partition, account, QoS, poll commands, cancellation command, default resources), station-level overrides (partition, account, QoS, CPUs, memory, walltime, extra allowed scheduler arguments, container image), rejection of unsupported or unsafe scheduler options, canonicalization of the `plain-slurm`→`slurm-native` and `docker-slurm`→`slurm-docker` aliases, and exposure of the effective executor mode.

#### Scenario: Station overrides instance defaults
- **GIVEN** instance-level SLURM defaults and a station-level override
- **WHEN** the effective executor configuration is computed
- **THEN** station values override instance values, unsafe options are rejected, and the effective mode is exposed, covered by `internal/executor/slurm_test.go`
### Requirement: Joborder Default Renderer

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Structured joborder shape
- **GIVEN** a station with joborder format `yaml`, `toml`, or `json`
- **WHEN** the job order is rendered
- **THEN** it is schema-valid with `veriproc_meta` and root workload fields as specified

#### Scenario: None mode contract
- **GIVEN** a station with joborder mode `none`
- **WHEN** dispatch occurs
- **THEN** no joborder artifact exists and any `<joborder>` argument reference fails before dispatch

**Additional normative rules for this requirement:**

The default joborder renderer MUST support `yaml`, `toml`, `json`, and `none`. For structured formats, VeriProc-owned identity and audit fields MUST be placed under `veriproc_meta` unless `joborder.meta: false` is declared, with workload-facing `order`, `inputs`, and `outputs` at the root, referencing the frozen task, run, station revision, processing window, resolved non-reserved `joborder.include` content, manifest reference where present, resolved inputs, and expected outputs. `joborder.meta: false` MUST omit only the serialized metadata block, not persisted audit records. For `none`, no joborder file or artifact is created, `VERIPROC_JOBORDER_PATH` is not injected, and `<joborder>` references fail before dispatch.
### Requirement: Joborder Template Renderer

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Missing input lookup fails
- **GIVEN** a template referencing an input lookup with no matching manifest entry
- **WHEN** rendering runs
- **THEN** the run fails before dispatch with an error naming the lookup

#### Scenario: Include rejected with template
- **GIVEN** a station using the template renderer with `joborder.include` configured
- **WHEN** the joborder configuration is validated
- **THEN** the configuration is rejected

**Additional normative rules for this requirement:**

The built-in template renderer (`joborder.renderer: template`) MUST support inline templates and station-directory-relative `template_file` loading, include template content in station revision identity, context-resolved `joborder.params`, input lookup helpers, path helpers, `yamlq`/`tomlq`/`jsonq` quoting helpers, declared format validation after rendering, failure on missing or ambiguous required input lookup, rejection of `joborder.include` with the template renderer, and processor-specific schemas without VeriProc root fields.
### Requirement: Joborder Include Merge

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Reserved field override rejected
- **GIVEN** a `joborder.include` leaf attempting to set the reserved `order` field
- **WHEN** the merge is performed
- **THEN** the merge fails and no job order is written

**Additional normative rules for this requirement:**

`joborder.include` MUST be context-resolved before merge, deep-merged deterministically for non-reserved mappings, support insertion of scalar and sequence fields, and allow fields such as `facility`, `dyn_params`, `temporary_folder`, `config_files`, and `log_level`. Overrides of reserved fields (`veriproc_meta`, `order`, `inputs`, `outputs`) or legacy top-level VeriProc metadata names (`schema_version`, `joborder_id`, `task_id`, `retry_index`, `run_ref`, `station`, `generator`, `manifest`) MUST be rejected.
### Requirement: Preprocess Script

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Prep variables exported
- **GIVEN** a preprocess script emitting `GAIN=2.5`
- **WHEN** the job order is rendered
- **THEN** the value is available as `<prep.GAIN>` or `.PrepVars["GAIN"]` in the appropriate renderer

#### Scenario: Invalid stdout line fatal
- **GIVEN** a preprocess script emitting unparseable stdout
- **WHEN** the run is processed
- **THEN** the run fails immediately with an error naming the script

#### Scenario: Missing input token fatal
- **GIVEN** `preprocess_args` containing `{input:FOO}` with no present `FOO` manifest entries
- **WHEN** dispatch is attempted
- **THEN** the run fails before the script is invoked

**Additional normative rules for this requirement:**

When `joborder.preprocess_script` is set, the script MUST execute after manifest and execution-context freeze but before job-order rendering. `KEY=VALUE` stdout lines MUST be made available as `<prep.KEY>` (default renderer) and `.PrepVars["KEY"]` (template renderer); blank lines and `#` comments are ignored; any other invalid stdout line causes immediate run failure naming the script; non-zero exit causes failure naming script and exit code. `{input:FILE_TYPE}` tokens in `preprocess_args` MUST be replaced by joined paths of present manifest entries of that type, failing if none are present. Context references in `preprocess_args` and the script itself are resolved against the station context, with `<instance_root>` resolving to the instance config directory. Preprocess failures MUST transition the run to `failed` and mark the task `failed`.
### Requirement: Local Log Capture

Local execution MUST capture standard output and standard error independently as `logs/run_out.log` and `logs/run_err.log`, and both MUST be represented as log artifacts for completed and failed runs.

#### Scenario: Independent logs
- **GIVEN** a local executor run writing to stdout and stderr
- **WHEN** the run finishes
- **THEN** two separate log files exist and are recorded as log artifacts
### Requirement: Completion and Output Validation

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Scheduler success without validation
- **GIVEN** a run whose executor reported terminal success but outputs are invalid
- **WHEN** finalization runs
- **THEN** the run transitions to failed, not `complete`

#### Scenario: Metadata without bytes
- **GIVEN** artifact bytes lost due to retention
- **WHEN** the artifact is queried
- **THEN** metadata is returned per retention policy while unavailable content is distinguishable

**Additional normative rules for this requirement:**

Scheduler success alone MUST NOT transition a run to `complete`. Completion requires output validation, persistence of required artifact metadata, canonicality decision, and downstream reconciliation or documented asynchronous routing policy. Mandatory outputs MUST be validated before completion; artifact records MUST identify producing run, logical artifact type, path or access reference, validation status, size and checksum metadata when available under policy, and publication status when applicable. Artifact bytes and artifact metadata MUST remain distinguishable; if bytes become unavailable, metadata MUST remain queryable per retention policy.
### Requirement: Output Directory Resolution

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Resolved directory used for validation
- **GIVEN** an output with `directory: <working_root>/custom` declared
- **WHEN** the run finalizes
- **THEN** the job order contains the resolved path and validation examines that directory

**Additional normative rules for this requirement:**

A declared output's `directory` field, when set, MUST be resolved via the station context before the job order is written and before output validation; the resolved path appears in the job order under `outputs[].directory`, and validation looks in that directory instead of the default `output/`. When absent, the default `<working_root>/output/` behaviour is preserved. Context references including `<working_root>` and `<instance_root>` MUST be supported in `directory` values. A declared output MAY be a directory recorded as an artifact with object-kind metadata; baseline completion MUST NOT require recursive child inspection or recursive checksum validation for directory outputs.
### Requirement: Publication Rules

Rolling-archive publication MUST default to the archive root without creating an implicit station-specific directory; an explicitly configured publication subpath MUST be used exactly and MUST be rejected if it would escape the archive root. When directory output publication is supported, directory outputs MUST be copied, moved, or symlinked at the directory root as opaque directory trees without per-child checksum verification.

#### Scenario: Subpath escape rejected
- **GIVEN** a publication subpath containing `..`
- **WHEN** publication is attempted
- **THEN** the path is rejected as escaping the archive root
### Requirement: Canonicality and Deduplication Invariants

At most one current canonical completed run MUST own a processing fingerprint under the default deduplication policy. A completed duplicate MUST NOT silently replace the canonical owner. Forced runs MUST be non-canonical by default unless an explicit promotion or replacement policy is implemented and verified. `latest_run` and `canonical_run` MUST be exposed distinctly through the API and CLI where applicable; the latest run MUST NOT be implied canonical.

#### Scenario: Forced rerun non-canonical
- **GIVEN** a canonical completed run for a fingerprint
- **WHEN** a forced rerun completes
- **THEN** the original canonical owner is preserved unless an explicit replacement policy applies

#### Scenario: Latest not canonical
- **GIVEN** a task whose latest run is a duplicate
- **WHEN** the task is queried
- **THEN** latest and canonical run references are distinct and correct
### Requirement: Idempotency and Retry Invariants

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Conflicting idempotent submission
- **GIVEN** an existing idempotency key
- **WHEN** a submission reuses it with different canonical content
- **THEN** the request is rejected as a conflict and no task is created, covered by HTTP API idempotency tests

**Additional normative rules for this requirement:**

Repeated task submission with the same idempotency key and the same canonical request content MUST return the same logical task result; with conflicting content it MUST be rejected as an idempotency conflict without creating or mutating a task. Retried preparation MUST NOT create duplicate active canonical work for the same task and fingerprint. Downstream task creation, artifact recording, rolling archive publication, split-group creation and readiness transitions, and deduplication record creation MUST be idempotent or protected by uniqueness constraints.
### Requirement: Scheduler Uncertainty and Recovery Invariants

If scheduler submission may have succeeded but the scheduler identifier is not durably persisted, restart MUST enter reconciliation or orphan-handling logic before any resubmission decision; the backend MUST NOT blindly submit a second job for the same run. Cancellation state MUST be persisted and recoverable; a crash during cancellation MUST NOT delete run, job, working-root, artifact, or provenance records.

#### Scenario: Uncertain submission reconciliation
- **GIVEN** a crash after a scheduler submit request with unknown outcome
- **WHEN** the backend restarts
- **THEN** the run enters reconciliation or orphan handling without duplicate submission
### Requirement: Provenance and Split-Group Invariants

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Provenance independent of history
- **GIVEN** task history made unreadable
- **WHEN** an artifact is queried
- **THEN** its producing run is still attributed via persisted provenance

#### Scenario: Aggregation from persisted records
- **GIVEN** a closed split group
- **WHEN** aggregation is materialized
- **THEN** the aggregation task references exactly one split group with persisted contributing artifacts, verified by `internal/groups` tests

**Additional normative rules for this requirement:**

Provenance links MUST be persisted independently of readable task history; API-returned artifacts MUST be attributable to their producing run; downstream tasks MUST have machine-queryable provenance links to the producing run. When supported, split-group membership, closure, readiness, grouped evaluation state, aggregation task creation, and aggregation input selection MUST be based on persisted split-group records and canonical child-task results — never on inferred filesystem state, archive scans, filename patterns, time-range overlap, or routing-history text. An aggregation task MUST reference exactly one split group and record selected contributing child artifacts and producing runs where available.
### Requirement: CLI Thin-Client Invariants

If a CLI is provided, tests MUST verify that it does not compute processing fingerprints, decide canonicality, evaluate split-group readiness, reconcile scheduler state, resolve rolling archive inputs, create downstream tasks, or mutate database records locally. CLI commands MUST map only to documented API operations and MUST NOT bypass the API through direct filesystem, scheduler, or database access.

#### Scenario: Submit uses API only
- **GIVEN** a CLI task submission
- **WHEN** the command executes
- **THEN** it performs the documented task-submission API operation and no direct control-plane mutation
### Requirement: MVP Submission and Preparation Scenarios

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Invalid ordering rejected pre-creation
- **GIVEN** a task submission with window end before start
- **WHEN** the submission is processed
- **THEN** it is rejected before any task record is created

#### Scenario: Non-identity working root
- **GIVEN** a prepared run
- **WHEN** its working root is stored
- **THEN** the path is derived from the effective template, remains under `working_root_base`, and run identity does not depend on it

**Additional normative rules for this requirement:**

The implementation MUST pass the minimum submission and preparation scenarios: valid task persisted before any run; idempotent retry semantics; invalid timestamp ordering, unknown/ambiguous destination rejected before task creation; force default `false`; run key `(task_id, retry_index)`; input resolution per configured categories and the classical matcher; frozen manifest with traceable working-root export; explicit missing optional inputs; failure before dispatch on missing mandatory input; fingerprint timing; unique working root produced by the effective template, contained under `working_root_base`, stored as a non-identity path; and schema-valid joborder before submission (or the recorded `none` contract).
### Requirement: MVP Dispatch, Finalization, and API Scenarios

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Job state exposure
- **GIVEN** a dispatched job
- **WHEN** job status is queried through the API
- **THEN** scheduler-derived state includes last-observed timestamps and is distinguishable from committed run state

**Additional normative rules for this requirement:**

The implementation MUST pass dispatch scenarios (stub dispatch, job and scheduler identity persisted as soon as known, job status with last-observed timestamps, uncertain-submission reconciliation, runtime environment injection) and finalization scenarios (no immediate completion on scheduler success, validation and artifact persistence before completion, validation failure transitions to failed, canonical owner creation or preservation, forced rerun does not replace canonical, canonical versus latest exposure, auditability across restart), and API scenarios (required success and machine-readable error bodies, pagination with deterministic ordering, committed database-backed reads, scheduler-derived state distinguished from committed state, tolerance of additive optional fields).
### Requirement: MVP CLI and Cancellation Scenarios

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Cancellation record preservation
- **GIVEN** a run cancellation request committed
- **WHEN** the run reaches `cancelled`
- **THEN** run, job, artifact, working-root, and provenance records remain queryable

#### Scenario: Restart after cancellation failure
- **GIVEN** a crash injected during the cancellation flow
- **WHEN** the backend restarts
- **THEN** cancellation state is reconciled without deleting audit records

**Additional normative rules for this requirement:**

When a CLI is claimed, the implementation MUST pass CLI scenarios (submission via API, all documented command equivalents, `--output json`/`yaml`/`table`, stdout/stderr separation, exit-code mapping, clear failure on unsupported backend API version, no access to working roots, archives, database, or scheduler as control-plane paths). The implementation MUST pass cancellation and restart scenarios: cancellation request accepted versus terminal `cancelled` distinguished, repeated cancellation per policy, records preserved after cancellation, and restart after failure injection at task creation, preparation, dispatch, finalization, and cancellation points.
### Requirement: API Compliance Requirements

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Pagination stability
- **GIVEN** a list endpoint under the tested consistency model
- **WHEN** pages are traversed
- **THEN** records are not skipped or duplicated and ordering is deterministic

#### Scenario: Checksum source distinction
- **GIVEN** an artifact with a checksum obtained from a sidecar
- **WHEN** artifact metadata is returned
- **THEN** the checksum source is distinguishable from a VeriProc-computed value

**Additional normative rules for this requirement:**

API compliance tests MUST verify version selection/signaling with clear failure on unsupported versions, existence of the endpoints for the claimed profile (task submission/query, run query, job query, artifact metadata, log access, relationships, cancellation where supported, health, readiness), the stable error model with machine-readable code, message, and correlation identifier, pagination with stable tie-breakers that does not skip or duplicate records, canonical/non-canonical distinctions, readable CLI-supporting fields (station, start, end, state, latest/canonical run reference, retry index, canonicality, created-at, working-root path) separate from canonical IDs, state consistency semantics per Section 5.6, artifact and log access behavior with object kind and unavailability distinctions, optional checksum fields with source distinction between sidecar-obtained and VeriProc-computed values, error payload stability across versions, and client tolerance of unknown optional fields.
### Requirement: Persistence and Migration Compliance

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Uniqueness constraint set
- **GIVEN** the relational store for a claimed profile
- **WHEN** constraint tests run
- **THEN** all listed uniqueness constraints are enforced, mapped to Go store constraint tests

#### Scenario: Projection rebuild
- **GIVEN** denormalized summary columns or materialized views
- **WHEN** projections are rebuilt from normalized state
- **THEN** they match authoritative task and station-revision records

**Additional normative rules for this requirement:**

Persistence tests MUST verify the required record classes for the claimed profile (tasks, task history, provenance, station revisions, runs, jobs, manifests and entries, artifacts, publication metadata, split groups, fingerprints, deduplication records, idempotency records, instance configuration identities, generation-hook identities), uniqueness constraints covering task IDs, run keys, job IDs, scheduler IDs when known, working-root paths, idempotency scope/key, retry indexes, manifest association, artifact identity, publication targets, split-group membership, and canonical deduplication ownership. Manifest and publication records MUST preserve filename metadata, interval policy, pattern identity, and root-relative targets. Denormalized or projected fields MUST be rebuildable from authoritative records and not treated as alternate identities; referential guarantees MUST hold; immutable/mutable boundaries MUST be enforced; transactions at external side-effect boundaries MUST support recovery; migrations MUST NOT reinterpret existing meaning and backfills MUST derive from authoritative normalized state; query projections MUST be rebuildable to match authoritative records.
### Requirement: Recovery and Reconciliation Compliance

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Artifact metadata gap recovery
- **GIVEN** a crash injected after output files exist but before artifact metadata commit
- **WHEN** the backend restarts
- **THEN** artifacts are recorded idempotently or the run is classified failed/incomplete, and completion is never reported from existing files alone

#### Scenario: Deduplication safety on crash
- **GIVEN** a crash injected during finalization before canonicality commit
- **WHEN** the backend restarts
- **THEN** at most one canonical owner exists for the fingerprint

**Additional normative rules for this requirement:**

Recovery tests MUST inject failures and verify safe restart behavior at each defined boundary: after task creation before run creation (no duplicate tasks on idempotent retry); after run creation before manifest freeze (resume, deterministic failure, or documented classification, never dispatch without a frozen manifest); after manifest freeze before scheduler acknowledgement (manifest and fingerprint preserved, input set unaltered); after uncertain scheduler submission (reconcile or orphan, never blind resubmission); after scheduler completion before validation (idempotent re-entry into finalization); after artifact bytes before metadata commit (idempotent recording, policy validation, or failure — never completion based only on existing files); during finalization before canonicality commit (no multiple canonical owners); after downstream task creation before completion (idempotent child reconciliation); during publication when supported (no duplicate logical publication records, no archive scanning as a substitute for durable records); during split-group lifecycle when supported (no unsafe ready marking, at most one aggregation task per group unless a verified re-aggregation policy exists); and during cancellation (preserved request state, reconciled job state). Failure injection points MUST be named in test evidence.
### Requirement: CLI Compliance Requirements

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Output mode variants
- **GIVEN** a CLI command with a structured payload
- **WHEN** run with `--output json`, `yaml`, or `table`
- **THEN** output uses stable field names and only primary payloads on stdout

#### Scenario: Unsupported API version
- **GIVEN** a CLI configured against a backend whose API version is unsupported
- **WHEN** a state-changing command runs
- **THEN** the CLI fails clearly before performing the operation

**Additional normative rules for this requirement:**

CLI tests MUST verify strict API-first behavior, command-to-API mapping for all documented operations, output modes with stable field names and no diagnostics on stdout, task-list and run-list readable column requirements from API fields, no parsing of station, window, retry, downstream, or provenance meaning from canonical IDs or working-root paths (permitted `station_id` prefix notwithstanding), standard stream behavior, exit-code mapping per Section 6.10 preserving the API error model, no local reimplementation of backend semantics, configuration and profile precedence order (defaults, config file, profile, environment, flags), clear failure on unsupported backend API version before state-changing operations, and non-mutating `config validate` behavior. Implementation status: CLI test coverage is partial relative to this section (covered: submit idempotency and timestamp forms, task get/list, run list/get, delete/clean dry-run flows, station, groups, output modes, and exit codes for usage, not-found, and conflict; not covered: `health`, `readiness`, `logs`, `artifact list`, `version --check-api` mapping, exit codes 3, 6, 7, 8, stream separation, configuration/profile precedence, and API-version-incompatibility failure).
### Requirement: Evidence and Reporting

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Scenario mapping
- **GIVEN** a claimed conformance profile
- **WHEN** the conformance report is reviewed
- **THEN** every mandatory Section 7.5 scenario maps to a test identifier or documented verification record

#### Scenario: Skipped mandatory test
- **GIVEN** a skipped test for a mandatory feature of the claimed profile
- **WHEN** conformance is assessed
- **THEN** it is a conformance gap unless the feature is explicitly deferred and the profile permits the deferral

**Additional normative rules for this requirement:**

An implementation claiming conformance MUST produce a reproducible conformance report including implementation name/version, specification revision, claimed profiles, tested executor configuration, database backends, API versions, CLI version and test status, automated test results with pass/fail/skip/not-applicable status, the minimum MVP scenario matrix status, migration results, crash-recovery and reconciliation results, a compatibility matrix, known deviations, explicitly deferred or unsupported features, and reproducible environment notes. Each claimed profile MUST map every mandatory Section 7.5 scenario to at least one automated test identifier or documented verification record. Skipped tests for mandatory features in a claimed profile MUST be treated as conformance gaps unless the feature is explicitly deferred and the profile permits it. The report MUST be produced automatically as part of release qualification; manual verification may supplement but not replace automated tests. Implementation status: no automated conformance-report or scenario-matrix artifact is generated by the current build; backend suites cover the major invariant areas, but there is no §7.5 scenario-to-test-ID mapping artifact and the smoke suite covers only backend start/shutdown.
