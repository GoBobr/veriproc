# Persistence Specification

## Purpose

This specification defines the normative persistence behavior of the VeriProc control-plane relational state store: identity models, immutability rules, cardinality and referential constraints, lifecycle persistence, idempotency and recovery protections, transactional rules, and schema evolution. It is derived from the former normative relational-data-model specification chapter (source document removed; see git history). The as-built physical schema (tables, columns, and implementation-status deviations) is documented in `docs/docs/14-data-model-reference.md`; column-level schema details are informative and are not requirements here. Scenarios are testable against the store tests in `internal/store/*_test.go`.

## Requirements

### Requirement: Database Authority Boundaries

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Query served from database after filesystem loss

- **GIVEN** a run whose task-out.yaml and manifest export files on the working root have been lost
- **WHEN** an API query requests the run's resolved inputs
- **THEN** the response is constructed from the persisted manifest header and entry records, without requiring the exported files

#### Scenario: Scheduler state does not override committed records

- **GIVEN** a run persisted as `cancelled`
- **WHEN** the scheduler reports a job still running for that run
- **THEN** the committed control-plane record remains authoritative and the run is not revived based on scheduler state

**Additional normative rules for this requirement:**

- Manifest records in the database are authoritative; a serialized manifest exported to the working root MUST be reproducible from or consistent with the database records and MUST NOT be the only durable source for API queries or recovery.
The database SHALL be authoritative for tasks, task status summaries, task history entries, provenance links, station revisions, runs, jobs, resolved input manifests and entries, artifact metadata, rolling-archive publication metadata, split groups and membership, processing fingerprints, deduplication records, and idempotency records. The shared filesystem SHALL remain authoritative for artifact bytes and directory trees; the database MUST store metadata sufficient to locate, identify, validate, and audit those filesystem objects, and MUST remain the system of record for API queries and recovery decisions.

- Filesystem state, scheduler state, and station-generated descriptors may inform reconciliation but MUST NOT replace committed control-plane records.
### Requirement: Single Canonical Identity Model

Each persisted domain concept SHALL have exactly one canonical identity model. Task identity SHALL be the single key `task_id`; job, artifact, split-group, station-revision, and processing-fingerprint identifiers SHALL remain distinct from task identity and from each other.

#### Scenario: Distinct identifiers do not collide

- **GIVEN** persisted tasks, jobs, artifacts, split groups, and station revisions
- **WHEN** identifiers are compared across record classes
- **THEN** no identifier from one record class is used to reference a record of another class
### Requirement: Run Composite Canonical Key

The canonical run key SHALL be the composite `(task_id, retry_index)`. `retry_index` is scoped to its parent task and MUST NOT be used alone as a foreign key, provenance reference, API identity, deduplication reference, or machine-queryable run reference. Readable context MUST be stored in first-class fields, not encoded into identifiers; the display form such as `scen2-20250703T111239000-a7sf7f/r5` is a rendering that MUST NOT become a second persisted identity.

#### Scenario: retry_index alone is not a run reference

- **GIVEN** a caller supplying only a retry index value
- **WHEN** attempting to reference a run
- **THEN** the reference is rejected as incomplete, because `(task_id, retry_index)` is required

#### Scenario: Display form is not a persisted identity

- **GIVEN** the display form of a run is rendered in an API response
- **WHEN** the run is queried again
- **THEN** the run is identified by `(task_id, retry_index)` (or the implementation's internal key mapping one-to-one to it), not by parsing the display string
### Requirement: Internal Run Key Constraint

An implementation MAY use an internal unique run key for physical database design. If present, it MUST be constrained so each internal key maps to exactly one `(task_id, retry_index)` and each `(task_id, retry_index)` maps to at most one active run record; it MUST NOT replace the composite key in normative relational semantics.

#### Scenario: Internal key maps one-to-one to composite key

- **GIVEN** a runs table using an internal run key
- **WHEN** two rows are inserted with the same `(task_id, retry_index)`
- **THEN** the unique constraint on `(task_id, retry_index)` rejects the second insert

#### Scenario: API projection preserves composite key

- **GIVEN** a run identified internally by a surrogate key
- **WHEN** the run is returned through the API
- **THEN** both `task_id` and `retry_index` are available in the projection

**Additional normative rules for this requirement:**

*Implementation status:* the implemented schema uses a surrogate `run_id` as the physical primary key of `runs`, with unique constraints on `(task_id, retry_index)` and `working_root`, and related tables reference runs by `run_id`; the composite key is preserved in API projections via the `run_ref` display form. The surrogate key is currently also the primary REST API resource identifier for runs (Known Issue).
### Requirement: Routing-Content and Task Immutability

Task destination station, processing window, force flag, parent references, split-group association, client metadata, and routing content MUST be immutable after task creation. Backend-maintained summary state, latest/canonical run references, failure-summary projection, and completion timestamp MAY be updated by the backend. The durable reason for a failed or cancelled attempt MUST be stored on the corresponding run record.

#### Scenario: Routing content rejected on update

- **GIVEN** a committed task
- **WHEN** an update attempts to change its processing window or destination station
- **THEN** the update is rejected and the original routing content is preserved

#### Scenario: Failure reason stored on run

- **GIVEN** a task whose latest run failed
- **WHEN** an operator queries the failure reason
- **THEN** it is read from the failing run record, not only from the task summary projection
### Requirement: Task Creation and Downstream Coupling

A task MUST be committed before any run is created for it. Downstream task creation MUST be transactionally coupled with provenance or relationship creation and any split-group membership records required for that child. Deterministic downstream task identifiers or downstream-creation keys MUST be unique.

#### Scenario: Run creation before task commit fails

- **GIVEN** a task insert that has not committed
- **WHEN** a run insert referencing that task is attempted in another transaction
- **THEN** referential integrity prevents the run from being committed first

#### Scenario: Downstream creation is atomic

- **GIVEN** a canonical run finalizing with a routing rule that creates a downstream task
- **WHEN** the downstream effect is committed
- **THEN** the child task, its provenance link, and any split-group membership row are committed together or the whole effect is rolled back
### Requirement: Join-Mode Downstream Identity

Join-mode downstream creation MUST derive the child task identity from the join declaration identity (`join_id`), target station, and target processing window, so independent upstream runs coalesce onto the same task. Each contributing run MUST create an idempotent provenance link to the shared task. Committing a new join contribution to a task in `waiting_inputs` MUST wake it for preparation without changing completed or already-running tasks.

#### Scenario: Independent runs coalesce onto one join task

- **GIVEN** two upstream runs declaring the same `join_id`, target station, and window
- **WHEN** both finalize and create downstream tasks
- **THEN** exactly one shared child task exists and each run has a distinct provenance link to it

#### Scenario: Join contribution wakes waiting task

- **GIVEN** a join-mode child task in `waiting_inputs`
- **WHEN** a new contributing run commits its contribution
- **THEN** the child task becomes eligible for preparation and already-completed or running tasks are unchanged
### Requirement: Deterministic Downstream-Creation Key

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Finalization retry does not duplicate children

- **GIVEN** a finalization that created a downstream task and then crashed before fully completing
- **WHEN** finalization is retried
- **THEN** the existing downstream task is discovered via the creation key and no duplicate task is created

#### Scenario: Non-canonical run emits no default downstream effects

- **GIVEN** a completed non-canonical run with no persisted policy authorizing non-canonical routing
- **WHEN** finalization completes
- **THEN** no default downstream tasks are created from it

**Additional normative rules for this requirement:**

Downstream task creation from a producing run MUST be protected by a unique downstream-creation key derived from the producing run, validated descriptor or station default routing rule, target station, target window, split-group membership key where applicable, and downstream declaration identity. Retrying finalization MUST NOT create duplicate downstream tasks. Only the canonical completed run MAY emit default downstream effects unless a persisted explicit policy authorizes non-canonical downstream creation.
### Requirement: Task Summary State Consistency

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Task summary reflects terminal run

- **GIVEN** a task whose latest run transitions to `complete` and acquires canonical ownership
- **WHEN** the task summary is queried
- **THEN** the summary state is consistent with the run and deduplication records

#### Scenario: History entries immutable

- **GIVEN** an existing task history entry
- **WHEN** a downstream task is created with copied history
- **THEN** the source entry is unchanged and the child's entries are extended only by insertion

**Additional normative rules for this requirement:**

Task state MUST summarize the routing request (accepted, active, completed canonically, failed, cancelled, waiting for retry, or superseded by policy) and MUST be maintained consistently with run and deduplication records, even when denormalized. Readable task history entries MUST be immutable after insertion and history extension for a downstream task MUST occur in the same transaction as downstream task creation or be recoverable without duplicate entries; history entries MUST NOT be the only persisted provenance mechanism.
### Requirement: Provenance Link Persistence

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Duplicate provenance link rejected

- **GIVEN** an existing provenance link with source S, target T, and relationship type R
- **WHEN** an identical link is inserted again
- **THEN** the uniqueness constraint rejects it

#### Scenario: Referential integrity for polymorphic links

- **GIVEN** a provenance link creation referencing a target object
- **WHEN** the referenced record does not exist and native foreign keys are unavailable
- **THEN** application-level transactional validation prevents the link from being committed and exposes a diagnostic

**Additional normative rules for this requirement:**

Provenance links MUST be machine-queryable, immutable after creation, and recorded at most once for the same source, target, and relationship type unless an explicit version or role distinguishes the records. Referential integrity MUST be enforced natively or through transactional application-level validation; links for downstream task creation, artifact recording, split-group membership, and aggregation MUST be created in the same transaction as the corresponding domain record or through idempotent reconciliation that cannot produce duplicates.
### Requirement: Station Revision Immutability

Station revision identity, station identifier, `station_name`, content hash, schema version, and validated configuration content MUST be immutable. Each active `station_id` and each active `station_name` MUST be unique within the deployment scope, and `(station_id, content_hash)` SHOULD be unique. A run MUST reference a validated station revision before preparation can proceed, and a revision referenced by a run MUST remain queryable for that run's retention period.

#### Scenario: Run references a validated revision

- **GIVEN** run preparation for a task
- **WHEN** the run record is created
- **THEN** it references an existing station revision and preparation cannot proceed otherwise

#### Scenario: Revision content cannot change

- **GIVEN** a station revision referenced by runs
- **WHEN** an update attempts to change its content hash or configuration content
- **THEN** the update is rejected

**Additional normative rules for this requirement:**

*Implementation status:* the implemented `station_revisions` table has no `validation_status` column and lacks the `station_name`, `content_hash`, and `effective_at` indexes as well as deployment-wide uniqueness of active `station_id`/`station_name` beyond `(station_id, content_hash)` (Known Issue).
### Requirement: Configuration Freezing on Runs

Every run MUST reference the exact station revision used to prepare it, and MUST reference the effective instance configuration identity and generation-hook identities when those affect execution context, audit, station context resolution, input resolution, joborder generation, execution arguments, or processing fingerprint computation.

#### Scenario: Fingerprint computation uses frozen configuration

- **GIVEN** a run being prepared under a configuration that affects output paths
- **WHEN** the processing fingerprint is computed
- **THEN** the fingerprint is derived using the frozen configuration identity rather than the current startup configuration

**Additional normative rules for this requirement:**

*Implementation status:* `instance_configuration_revisions` and `generation_hook_identities`, and their run references, are not implemented in the current baseline; effective instance configuration is loaded at startup and generator identities are recorded as opaque strings in run metadata. These are retained as the target model and tracked as Known Issues.
### Requirement: Run Field Immutability

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Working root uniqueness on assignment

- **GIVEN** two runs in preparation
- **WHEN** both are assigned the same working-root path
- **THEN** the uniqueness constraint rejects the second assignment

#### Scenario: Working root is opaque

- **GIVEN** a stored working-root path of arbitrary depth
- **WHEN** the run's identity or station association is needed
- **THEN** it is obtained from run columns and foreign keys, never by parsing the path

**Additional normative rules for this requirement:**

Task identifier, retry index, station revision, working-root path after assignment, generator identities after assignment, and processing fingerprint after computation MUST be immutable on a run. State, active job reference, finalization summary, failure reason, canonical classification until terminal finalization, and timestamps MAY change according to the documented lifecycle. `working_root_path` MUST be unique once assigned and MUST be treated as an opaque stored path, with no identity, station, task, retry, or provenance semantics derived by parsing path components or assuming a fixed depth.
### Requirement: Run Lifecycle State Vocabulary

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Scheduler success does not complete a run

- **GIVEN** a run whose scheduler job reports successful completion
- **WHEN** output artifacts have not been validated and persisted
- **THEN** the run is not transitioned to `complete`

#### Scenario: Lifecycle blocks invalid transitions

- **GIVEN** a run in a terminal state
- **WHEN** an update attempts a non-terminal state value
- **THEN** the lifecycle constraint refuses the mutation

**Additional normative rules for this requirement:**

*Implementation status:* the implemented vocabulary is exactly the nine states above; there are no `dispatching` or `orphaned` states, and lifecycle is enforced conditionally in SQL (`runs_lifecycle`).
Run state MUST preserve the internal lifecycle needed for recovery, at minimum distinguishing `pending`, `preparing`, `ready`, `dispatched`, `running`, `finalizing`, `complete`, `failed`, and `cancelled` (or a documented equivalent with the same recovery behavior). Job state MUST store scheduler-facing status separately from run state; scheduler-native state MAY lag run state and MUST NOT be treated as sufficient for run completion without output validation and artifact persistence. Split-group closure, readiness, and grouped evaluation state MUST remain recoverable, even if a composite group state is stored for query efficiency.
### Requirement: Terminal-State Immutability and No Revival

Terminal states MUST record terminal timestamps and SHOULD record terminal reasons or summaries. Terminal run states MUST NOT be changed except by explicit operator or API actions that create auditable promotion, supersession, or correction records. Retrying a task MUST create a new run, not mutate a terminal run back into a non-terminal state.

#### Scenario: Retry creates a new run

- **GIVEN** a run in terminal state `failed`
- **WHEN** the task is retried
- **THEN** a new run with the next `retry_index` is created and the failed run is unchanged

#### Scenario: Terminal run not revived

- **GIVEN** a run in state `complete`
- **WHEN** any non-operator process attempts to set it to `running`
- **THEN** the transition is refused
### Requirement: Retry-Safe Run Creation

Run creation for a task retry MUST be protected by the unique task-scoped key `(task_id, retry_index)` and by any equivalent retry-request key used for recovery safety. Repeated identical run-creation operations on recovery MUST return or resume the existing run, and a retry MUST never reuse a previous run's working root.

#### Scenario: Concurrent retries allocate distinct retry indexes

- **GIVEN** two concurrent retry requests for the same task
- **WHEN** both attempt run creation with the same `retry_index`
- **THEN** the uniqueness constraint allows only one to succeed

#### Scenario: Recovery resumes existing run

- **GIVEN** a crash after run creation committed but before dispatch
- **WHEN** the same run-creation operation is repeated
- **THEN** the existing run is returned rather than a duplicate being created
### Requirement: Jobs Persistence and Scheduler-Id Timing

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Scheduler id persisted as soon as known

- **GIVEN** a job submitted to the scheduler
- **WHEN** the scheduler assigns an identifier
- **THEN** the jobs record persists that identifier before further run-state transitions

#### Scenario: Uncertain submission is reconciled before resubmission

- **GIVEN** a submission whose acknowledgement outcome is uncertain after a restart
- **WHEN** the backend considers resubmission
- **THEN** it first reconciles with scheduler-visible state or enters a reconciliation path instead of blindly submitting another job

**Additional normative rules for this requirement:**

*Implementation status:* the implemented `jobs` table enforces neither `UNIQUE(executor, scheduler_id)` nor `UNIQUE(run_id, submission_attempt)`; `scheduler_id` has a plain index and the at-most-one-active-job rule is not schema-enforced (Known Issue). There is also no submission-uncertainty marker or `orphaned` state: runs left in `ready` by a crash are resubmitted unconditionally on restart (submission-uncertainty Known Issue), while the reconciler covers stale observations only for `dispatched`/`running` runs with job records.
A job record MUST persist job identifier, run reference, executor type, submission attempt number, submission timestamp, and the scheduler identifier once known or an explicit pending/unknown marker before it is known. Job identifier, run reference, executor type, and submission attempt number MUST be immutable; the scheduler identifier MUST be immutable once known. `(executor_type, scheduler_identifier)` MUST be unique when the scheduler identifier is known, `(run_task_id, run_retry_index, submission_attempt_number)` MUST be unique, and a run MUST have at most one active non-terminal job under the default policy. The backend MUST persist scheduler identity as soon as it is known, and if submission outcome is uncertain, MUST capture reconciliation status before any resubmission decision.
### Requirement: Manifest Freezing

Each run MUST have at most one frozen resolved input manifest under the default preparation model; the manifest header (run reference, schema version, resolution policy, creation and freeze timestamps) MUST be immutable once frozen, with the export artifact reference addable afterwards only if it does not change manifest meaning. A run MUST NOT enter `ready` until its resolved input manifest is persisted and frozen.

#### Scenario: Run not ready before frozen manifest

- **GIVEN** a run in `preparing` whose manifest has not been frozen
- **WHEN** a transition to `ready` is attempted
- **THEN** the transition is refused until the manifest header and entries are committed

#### Scenario: One frozen manifest per run

- **GIVEN** a run with an existing frozen manifest
- **WHEN** a second manifest header for the same run is inserted
- **THEN** the at-most-one constraint rejects it

**Additional normative rules for this requirement:**

*Implementation status:* the implemented manifest header records run, folder sources, and selection results, but does not persist `schema_version`, creation timestamp, resolution-policy identity, or content hash; manifest exports exist as `artifacts` rows of logical type `manifest_export` but are not linked back to the header.
### Requirement: Manifest Entry Persistence

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Missing optional input recorded explicitly

- **GIVEN** a station declaring an optional input with no matching filesystem object
- **WHEN** the manifest is frozen
- **THEN** an entry exists for that declared input with a `missing_optional` outcome

#### Scenario: Checksums not computed under available_only

- **GIVEN** the default integrity policy and a resolved regular-file input without an available checksum
- **WHEN** the manifest entry is persisted
- **THEN** the checksum fields remain null and the file contents are not read solely to compute a checksum

#### Scenario: Structured selection explainable from entry

- **GIVEN** an entry selected via structured filename matching among competing folder sources
- **WHEN** an auditor inspects the persisted entry
- **THEN** the pattern, window-match policy, parsed components, interval grouping key, folder priority, and winner comparison metadata explain the selection without re-running discovery

#### Scenario: Multi-object input preserves multiplicity

- **GIVEN** a declared input resolving to three filesystem objects
- **WHEN** the manifest is persisted
- **THEN** three bindings are stored in deterministic order and each is independently reportable

**Additional normative rules for this requirement:**

- Selected inputs MUST record source identity and precedence, rolling archive identifier when applicable, physical source path, working-root input path, and object kind supporting at least `regular_file` and `directory`.
Manifest entries MUST record every declared station input, including missing optional inputs, with per-declared-input outcome and selected filesystem-object bindings; entries MUST be immutable once the manifest is frozen. Multi-object inputs MUST preserve ordering and multiplicity and each selected object MUST be independently reportable by the API and audit model. One entry per declared station input and each selected binding MUST be preserved, with structured-filename bindings for one declared input distinguishable by logical interval identity or equivalent grouping metadata.

- Under the default integrity policy `available_only`, checksum columns remain nullable; the backend MUST NOT compute checksums by reading full regular-file contents solely to populate them, and directory inputs MUST NOT require checksums or be recursively walked solely to compute a digest. Size, modification time, version metadata, object kind, and selection metadata SHOULD still be recorded when available.
- When input selection depends on the structured filename contract, entry persistence MUST preserve the effective `filename_pattern`, any `<FILE_TYPE>` substitution, the applied `window_match` policy, parsed filename components, logical interval grouping key, source folder priority, and winner comparison metadata sufficient to explain the selection without re-running discovery logic.

*Implementation status:* implemented entries have no `declared_input_key` column (identity is `file_type`/`category`/`entry_id`), represent the outcome as a boolean `present` flag rather than `resolved`/`missing_optional` values, store one `path` column without a source-path vs working-root-input-path distinction, and lack `file_type`/`category` indexes.
### Requirement: Artifact Recording

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Duplicate logical artifact prevented

- **GIVEN** an existing artifact for run R, logical type T, and path P
- **WHEN** a finalization retry inserts the same logical artifact
- **THEN** the uniqueness constraint (or application-level equivalent) prevents a duplicate record and validation continues on the existing one

#### Scenario: Complete requires validated mandatory artifacts

- **GIVEN** a run whose mandatory outputs have not been validated
- **WHEN** a transition to `complete` is attempted
- **THEN** the transition is refused

#### Scenario: Job-order artifact cardinality

- **GIVEN** a station declaring `joborder.format: yaml`
- **WHEN** job-order creation succeeds
- **THEN** exactly one job-order artifact record exists for the run; for `joborder.format: none` no job-order artifact record exists

#### Scenario: Directory artifact recorded as one root

- **GIVEN** a station declaring a directory output
- **WHEN** the run finalizes
- **THEN** one artifact record rooted at the directory path is created without recursive child inspection or per-child checksums

**Additional normative rules for this requirement:**

*Implementation status:* the implemented table has no `UNIQUE(producing_run_id, logical_type, path)` constraint (duplicates prevented by application logic only), lacks the `logical_type`/`object_kind`/`file_type`/`validation_status`/`checksum` indexes and storage-role and validation-timestamp columns, and the exactly-one-job-order rule is not schema-enforced (Known Issue).
Artifact records MUST store logical type, filesystem path, object kind (supporting at least `regular_file` and `directory`), run association or external origin, and sufficient metadata to evaluate availability and validation status. Artifact identifier, producing run reference, logical type, filesystem path, and object kind MUST be immutable; validation/checksum metadata and publication summary MAY be updated during finalization only for the same filesystem-object identity under the effective integrity policy. `(producing_run_task_id, producing_run_retry_index, logical_artifact_type, path)` MUST be unique and finalization retries MUST be able to discover the existing record and continue. A run MUST have exactly one job-order artifact after job-order creation succeeds for `yaml`, `toml`, or `json` formats and no job-order artifact when the station declares `joborder.format: none`. A run MUST NOT reach `complete` until required output artifacts have been validated and their records persisted. Directory output artifacts MUST be recorded as one logical artifact rooted at the directory path, and baseline finalization MUST NOT require recursive child inspection or recursive checksum verification for directory artifacts. Artifact metadata MUST be retained at least as long as the producing run; if bytes are removed by retention policy, the record SHOULD remain with a retention or availability marker.
### Requirement: Publication Persistence

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Publication is idempotent after crash

- **GIVEN** a crash after the target bytes were created but before the publication record committed
- **WHEN** publication is retried
- **THEN** the existing target is detected and one logical publication record results, not two

#### Scenario: Target path has no implicit station directories

- **GIVEN** a publication with no configured subpath
- **WHEN** the target path is stored
- **THEN** it resolves to the published filename at the archive root with no station-derived prefix

#### Scenario: Publication before dependent downstream commit

- **GIVEN** a downstream task that consumes a published artifact
- **WHEN** the downstream task is committed
- **THEN** the publication record it depends on has already committed successfully

**Additional normative rules for this requirement:**

*Implementation status:* the implemented table enforces `UNIQUE(artifact_id, archive_id, target_path)` and stores `target_path` relative to the archive root, but lacks the `archive_id`/`target_path` indexes and the `retry_count`/`retention_policy` columns; the asynchronous publication worker resolves pending records against a single configured archive base (single-archive-base Known Issue).
Publication records MUST link exactly one source artifact to exactly one target rolling archive, and `target_path` MUST be interpreted relative to the selected archive root, by default resolving to the published filename at that root; additional directory segments MUST come only from an explicit persisted publication subpath or equivalent station policy, without implicit station-derived directory prefixes. `(source_artifact_id, rolling_archive_id, target_path)` MUST be unique. Publication MUST be idempotent: if bytes are created before the database commit, recovery MUST be able to detect the existing target and record or validate the publication without creating a duplicate logical publication. Source artifact, producing run, archive identifier, target path, object kind, and publication mode MUST be immutable once publication succeeds; publication state, failure reason, retry count, and timestamps MAY change while pending or being retried. Publication records required for downstream consumption MUST be committed before dependent downstream tasks are committed. Publication MUST NOT change the producing-run identity of the source artifact, and published bytes removal MUST NOT remove the source artifact record. Directory publication identifies one logical published directory root and may copy, move, or (under deployment policy) symlink the tree; recursive hardlinking or per-child checksum verification is not required.
### Requirement: Split-Group and Membership Persistence

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Duplicate aggregation task prevented on retry

- **GIVEN** a group that has reached readiness and materialized an aggregation task
- **WHEN** readiness evaluation is retried
- **THEN** no second aggregation task is created for the group

#### Scenario: Readiness from membership records only

- **GIVEN** a group whose artifacts exist in an archive but whose membership records show one member not yet canonically complete
- **WHEN** readiness is evaluated
- **THEN** the group is not ready, because persisted membership records are authoritative

#### Scenario: Membership unique per child

- **GIVEN** a child task already active in one split group
- **WHEN** it is added to a second active group
- **THEN** the insert is rejected unless an explicit multi-group policy is persisted

#### Scenario: Membership update consistent with canonicality

- **GIVEN** a group member whose selected contribution is being recorded
- **WHEN** membership is updated
- **THEN** the contributing run reference matches that child task's canonical completed run

**Additional normative rules for this requirement:**

*Implementation status:* the implemented `split_groups` table is a simplified coordination record carrying group id, expected member count, canonical/failed counters, and a single `state` column (default `open`), rather than the separate parent-window, evaluation-mode, closure/readiness/evaluation states, policy identities, failure reason, and timeout/partial-aggregation policies described above (Known Issue).
The split-group record MUST persist parent task or parent run association, parent processing window, group evaluation mode, grouped evaluation target or stage, membership basis, closure state, readiness state, grouped evaluation state, readiness policy identity, and creation timestamp; group identifier, parent association, parent window, evaluation mode, evaluation target, membership basis, and aggregation target MUST be immutable after creation, and membership MAY change only until closure, which is irreversible unless an explicit audited operator correction policy exists. Membership records MUST persist child task identity, a deterministic child key or member sequence, membership state, readiness contribution state, contributing canonical run reference when available, selected contributing artifacts, duplicate-handling outcome, and exclusion state and reason when partial aggregation is allowed; `(group_id, child_key)` and `(group_id, child_task_id)` MUST be unique and a child task MUST belong to at most one active split group unless an explicit multi-group policy is persisted. Split-group creation MUST be transactionally coupled with child-task membership creation when membership is known at fan-out time; readiness transition to ready MUST be transactional with the evaluated membership snapshot, and aggregation task creation MUST be protected so that only one aggregation task exists per group under the default policy even if readiness evaluation is retried. A producing canonical run MUST NOT create duplicate split groups for the same declared group key. Readiness evaluation MUST be performed per `group_id` from persisted split-group records, MUST NOT infer aggregation membership from archive scanning, filename patterns, overlapping time ranges, station identity, product type, or routing-history text, and membership updates selecting a canonical child result MUST be consistent with the child task's canonical completed run. For aggregation-mode groups the aggregation task MUST reference exactly one split group, the aggregation run's manifest MUST record the contributing child task, producing run, and artifact identity for each selected input where known, and canonicality changes after aggregation task materialization MUST NOT alter the aggregation input set unless an explicit persisted re-aggregation policy is invoked.
### Requirement: Deduplication Ownership

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Second canonical owner rejected

- **GIVEN** a fingerprint with a current canonical owner
- **WHEN** another run finalizes with the same fingerprint without replacement authorization
- **THEN** the later run is recorded as non-canonical (or failed per policy) and the ownership transaction commits atomically with its classification

#### Scenario: Promotion records history

- **GIVEN** an explicit promotion of a non-canonical run to canonical owner
- **WHEN** the operation commits
- **THEN** previous owner, new owner, timestamp, and reason are recorded

**Additional normative rules for this requirement:**

*Implementation status:* the implemented table omits the `canonical status` field, superseded timestamp, and fingerprint algorithm version; ownership history is delegated to the separate `canonicality_audit` table, a schema extra exceeding this specification.
One processing fingerprint MUST have at most one current canonical owner; if a canonical owner already exists and the current run is not authorized to replace it, the current run MUST be recorded as non-canonical or failed according to policy and MUST NOT emit default downstream effects. Creating or updating a deduplication record MUST occur in the same transaction as canonical run classification and before default downstream effects are emitted, and finalization MUST guarantee transactionally that no two runs become current canonical owners for the same fingerprint. Ownership metadata MAY change only through explicit promotion, replacement, or supersession operations that preserve previous ownership history; supersession or promotion MUST record previous owner, new owner, timestamp, reason, and actor or operation source where available. A run MUST NOT own more than one canonical deduplication record unless an explicit multi-fingerprint policy is defined; deduplication records MUST NOT be deleted in a way that permits accidental duplicate canonical dispatch. If finalization cannot determine canonical ownership safely, the run MUST NOT be marked canonical and MUST remain in `finalizing`, fail with a diagnostic, or enter an equivalent-safe recovery state.
### Requirement: Idempotency Records

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Exact retry returns recorded result

- **GIVEN** an idempotency key with a recorded task result and an identical canonical request hash
- **WHEN** the submission is retried
- **THEN** the recorded logical task result is returned and no second task is created

#### Scenario: Conflicting retry fails

- **GIVEN** an idempotency key already used with request content A
- **WHEN** a submission reuses the key with different canonical request hash B
- **THEN** the submission fails as an idempotency conflict without creating a task

**Additional normative rules for this requirement:**

*Implementation status:* the implemented table stores scope, key, request hash, task reference, timestamps, and expiry with `UNIQUE(scope, key)` and an expiry sweep, but omits the `operation_type` and result-status columns and the `request_hash`/`expires_at` indexes.
Task submission MUST be protected by a unique `(scope, idempotency_key)` constraint including a canonical request hash: an exact retry MUST return the same logical task result, and a conflicting retry with the same key and different canonical request hash MUST fail transactionally without creating a second task. Scope, key, request fingerprint, and operation type MUST be immutable after first insert; result status and response summary MAY be updated on completion. Records MUST be retained for at least the documented retry window, and expired records MAY be removed only when removal cannot create ambiguity for supported client retries. If a client provides no idempotency key and the API permits it, the backend MAY create a new task per accepted request, documented by the API.
### Requirement: Fingerprint Prerequisite

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Run not dispatched without fingerprint

- **GIVEN** a run whose fingerprint computation failed
- **WHEN** dispatch is considered
- **THEN** the run is not eligible and remains out of the dispatch path

#### Scenario: Dispatch awaits persisted fingerprint

- **GIVEN** a run whose fingerprint is computed but not yet committed
- **WHEN** a canonical dispatch decision is evaluated
- **THEN** the decision waits until the fingerprint is persisted

**Additional normative rules for this requirement:**

Every prepared run MUST have exactly one processing fingerprint unless preparation fails before computation; the fingerprint MUST be computed during preparation after station revision resolution, station context resolution, input manifest resolution, relevant configuration and generator identities, joborder format and include content, and resolved execution arguments are frozen, and before the run is eligible for dispatch. The fingerprint value, algorithm version, and canonicalized inputs MUST be immutable after computation, MUST be indexed for deduplication lookup, and MUST be available before finalization performs canonicality or deduplication ownership decisions. If the fingerprint cannot be computed or persisted, the run MUST NOT be dispatched. Canonical dispatch and finalization deduplication decisions MUST use the stored fingerprint and MUST NOT assume ownership before the fingerprint is persisted.
### Requirement: Canonicality Classification Persistence

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Forced rerun completes non-canonically

- **GIVEN** a task with a canonical completed run and a forced rerun
- **WHEN** the forced rerun completes successfully
- **THEN** it is persisted as non-canonical and the original owner is unchanged

#### Scenario: API classification derivable

- **GIVEN** a completed run and its deduplication record
- **WHEN** the classification is requested via the API
- **THEN** the value is computed from persisted records alone

**Additional normative rules for this requirement:**

Canonical classification MUST be persisted on the run or an associated canonicality record, distinguishing canonical, non-canonical forced rerun, superseded canonical owner, or not applicable, and the API-visible classification MUST be derivable from persisted run and deduplication records. A run that completes and becomes the deduplication owner for its fingerprint is canonical; a forced rerun MUST be non-canonical by default even if it completes successfully; a non-canonical run MUST NOT own the canonical deduplication record unless explicitly promoted or used in an explicit replacement policy.
### Requirement: Cardinality and Referential Rules

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Cardinality violations rejected

- **GIVEN** inserts violating any listed cardinality (e.g., a job referencing two runs, or a second active job for a run)
- **WHEN** the insert is attempted
- **THEN** a unique or referential constraint rejects it

#### Scenario: Dangling reference prevented

- **GIVEN** a required relationship whose target row does not exist
- **WHEN** the referencing row is committed
- **THEN** referential integrity (native or transactional) prevents it and a diagnostic is available

**Additional normative rules for this requirement:**

The relational model MUST enforce or validate the following cardinalities: one task MAY have zero or more runs; one run MUST reference exactly one task, exactly one station revision, and own one distinct working root once preparation materializes filesystem state; one run MUST have zero or one active scheduler job at a time under the default policy; one job MUST reference exactly one run; one run MUST have at most one frozen manifest; one manifest MUST contain one entry per declared station input preserving multiplicity; one VeriProc-produced artifact MUST reference exactly one producing run; one publication MUST reference exactly one source artifact and one target rolling archive; one split group MUST reference one parent task or parent run and MAY have zero or more member child tasks, with a member child appearing at most once per group, at most one active group per child absent a multi-group policy, at most one aggregation task per group under default policy, and exactly one split group per aggregation task; one fingerprint MAY be associated with multiple runs but MUST have at most one current canonical deduplication owner; one canonical completed run MAY drive default downstream effects for its task and non-canonical runs MUST NOT unless an explicit policy permits. Referential integrity MUST prevent dangling references for required relationships; where a database cannot enforce a polymorphic relationship natively, the implementation MUST enforce it transactionally and expose diagnostics for invalid references.
### Requirement: Recoverability of Side-Effect Transitions

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Restart mid-finalization recovers safely

- **GIVEN** a crash during finalization after some downstream effects committed
- **WHEN** the backend restarts and reconciles the run
- **THEN** idempotency keys cause the existing effects to be discovered and no duplicates are created

#### Scenario: Audit explains run preparation

- **GIVEN** a completed run
- **WHEN** an auditor queries persistence
- **THEN** station revision, selected and missing inputs, validated outputs, created downstream tasks, and the canonical owner are all recoverable from the database

**Additional normative rules for this requirement:**

State transitions that cross external side effects MUST be persisted so that preparation, dispatch, finalization, downstream creation, artifact recording, rolling-archive publication, split-group readiness, and deduplication ownership are recoverable or safely classifiable after backend restart. Retained audit metadata MUST be sufficient to explain why a run was prepared, which station revision and instance configuration were used, which inputs were selected or missing, which outputs were validated, which downstream tasks were created, and which run owns canonical status for a fingerprint.
### Requirement: Normalization and Projections

Implementations MAY add denormalized summary columns or projections for efficient API queries, but they MUST be derived from normalized records, MUST NOT be the only durable representation of required relationships, and MUST be rebuildable from normalized state or markable stale if inconsistent. Correctness requirements such as canonical ownership, split-group readiness, and downstream idempotency MUST rely on normalized transactional state, not projections.

#### Scenario: Projection rebuild after inconsistency

- **GIVEN** a task summary projection inconsistent with normalized run records
- **WHEN** recovery runs
- **THEN** the projection is rebuilt from normalized state or marked stale until rebuilt

#### Scenario: Readiness not decided from projection

- **GIVEN** a stale split-group summary projection showing ready
- **WHEN** readiness correctness matters
- **THEN** the decision uses normalized membership records
### Requirement: Scheduler Submission-Uncertainty Persistence

Scheduler submission MUST be persisted so the backend can distinguish, after restart: no submission attempted; submission attempted but acknowledgement uncertain; scheduler identifier known and persisted; scheduler reconciliation required; and scheduler job confirmed absent or found.

#### Scenario: Uncertain submission distinguished from none

- **GIVEN** a restart after a submission attempt with no acknowledgement
- **WHEN** the run is examined
- **THEN** the persisted state distinguishes "attempted, uncertain" from "not attempted" and drives reconciliation rather than unconditional resubmission

**Additional normative rules for this requirement:**

The backend MUST NOT blindly submit another job for the same run after uncertainty; it MUST first reconcile with scheduler-visible state when possible or enter an implementation-defined reconciliation path.


*Implementation status (Known Issue):* not implemented — see the jobs requirement above; runs left in `ready` by a crash are resubmitted unconditionally on restart, which can duplicate a submission with uncertain outcome.
### Requirement: Retention and Deletion Constraints

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Task not deleted while referenced

- **GIVEN** a task referenced by runs and provenance links
- **WHEN** deletion is attempted
- **THEN** it is refused while any reference exists

#### Scenario: Byte cleanup preserves metadata

- **GIVEN** retention cleanup removing working-root artifact bytes
- **WHEN** cleanup completes
- **THEN** the artifact records remain with availability markers and audit metadata intact

**Additional normative rules for this requirement:**

Task, run, and job records are durable audit records and MUST NOT be physically deleted while referenced by runs, jobs, provenance links, artifacts, publications, split groups, or deduplication records; implementations MAY support archival or tombstoning if API queryability and referential integrity are preserved. Station revisions, configuration revisions, hook identities, manifests, entries, artifacts, publications, split groups, and memberships must be retained per the retention rules stated with their record types. Retention cleanup MAY remove artifact bytes only according to deployment policy and MUST NOT silently remove or corrupt the durable metadata required for audit, provenance, deduplication, or split-group aggregation history.
### Requirement: Schema Evolution and Migration Rules

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Migration preserves run identity

- **GIVEN** a database with existing runs identified by `(task_id, retry_index)`
- **WHEN** a schema migration is applied
- **THEN** all existing identifiers and composite run keys remain stable and run meaning is unchanged

#### Scenario: Destructive migration requires operator action

- **GIVEN** a destructive migration script
- **WHEN** it is executed
- **THEN** it proceeds only after explicit operator action and leaves an audit path or backup for rollback

#### Scenario: Fingerprint contract change is versioned

- **GIVEN** a migration that alters fingerprint computation
- **WHEN** it completes
- **THEN** the fingerprint algorithm is versioned and pre-existing run fingerprints are not retroactively reinterpreted

**Additional normative rules for this requirement:**

The relational schema MUST have an explicit schema version and migration policy. Migrations MUST NOT change the meaning of existing runs, MUST keep existing task identifiers and task-scoped run keys `(task_id, retry_index)` stable, MUST keep required audit and query fields queryable, MUST be deterministic and repeatable when transforming normalized records, MAY add nullable fields, optional records, or enum values only when older clients and recovery logic can handle their absence or unknown values, and MUST require explicit operator action plus an audit path or rollback backup for destructive changes. If a migration changes fingerprint computation, job-order schema interpretation, station revision interpretation, or API-visible lifecycle semantics, the affected contract MUST be versioned and existing runs MUST NOT be retroactively reinterpreted without an explicit migration rule.
### Requirement: Implementation Schema Extras

The implemented database includes tables exceeding this specification: `station_controls`, `api_credentials`, `rate_limit_buckets`, `canonicality_audit`, and `join_arrival_count`.
These tables are implementation extras and MUST NOT weaken any requirement of this specification; `canonicality_audit` carries deduplication ownership-history duties described in the deduplication requirement, and `join_arrival_count` supports join-mode contribution bookkeeping described in the join-mode requirement.

#### Scenario: Schema extras do not weaken constraints

- **GIVEN** the implemented schema with its extra tables
- **WHEN** any requirement of this specification is evaluated
- **THEN** the extra tables neither replace nor relax the normative identity, immutability, uniqueness, or transactional rules
