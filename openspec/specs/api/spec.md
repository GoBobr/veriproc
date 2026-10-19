# REST API and Query Semantics Specification

## Purpose

Specify the normative REST API contract for VeriProc clients and operators, based on the former REST API specification chapter (chapter 5; source document removed; see git history). The API is the only supported control-plane interface for submitting tasks, querying state, requesting cancellation, and inspecting provenance. JSON payload examples in the source document remain informative reference material and are not reproduced here. Unless explicitly noted as implementation status, all requirements are normative.

## Requirements

### Requirement: API Scope And Control-Plane Exclusivity
The REST API SHALL be the only supported control-plane interface. Clients MUST NOT mutate backend state by writing directly into working roots, modifying database records, changing rolling-archive publication metadata, or editing station-derived control artifacts.

#### Scenario: Direct Backend Mutation Forbidden
- **GIVEN** a client that wants to change task state
- **WHEN** the client writes directly into a working root or database record instead of using the API
- **THEN** the mutation is outside the supported control plane and the behavior is undefined by this specification

#### Scenario: Control Operations Via API
- **GIVEN** a conforming backend
- **WHEN** a client submits a task, queries state, requests cancellation, or inspects provenance
- **THEN** the backend exposes these operations through the REST API

### Requirement: API Version And Documentation
The API MUST expose an explicit version (in URL path, media type, headers, or another documented mechanism). A conforming implementation MUST document which API versions it serves and which compatibility guarantees apply to each version.

#### Scenario: Versioned API Surface
- **GIVEN** a conforming backend
- **WHEN** a client inspects API endpoints
- **THEN** every endpoint is addressable under an explicit documented API version


**Additional normative rules for this requirement:**

**Implementation status.** All endpoints are served under the `/api/v1/` prefix. A `/api/v1/version` path is excluded from authentication but no version handler is currently routed; version information is exposed through health/readiness endpoints instead.

### Requirement: Identity Distinction Preservation
The API MUST preserve the conceptual distinction between tasks, runs, jobs, job orders, artifacts, split groups, provenance links, station revisions, and processing fingerprints. Representations MAY be optimized for clients but MUST NOT conflate these identities.

#### Scenario: Distinct Resource Identities
- **GIVEN** an API response containing task, run, and job data
- **WHEN** the client parses the response
- **THEN** task, run, and job identities are distinguishable and not merged into a single identity

### Requirement: Canonical Resource Identifiers
API resource identifiers MUST use the canonical identity model. Task resources use `task_id`. Run resources use the task-scoped composite `(task_id, retry_index)`. A preferred run display form such as `scen2-...-a7sf7f/r5` MAY appear in responses for readability, but it is a rendering of the composite, not a separate API identity.

#### Scenario: Run Identity Fields In Response
- **GIVEN** a run resource returned by the API
- **WHEN** the client inspects it
- **THEN** the response identifies the run through `task_id` and `retry_index` (and the implemented `run_id` addressing) and MAY include a `run_ref` display string

#### Scenario: Display Form Not An Identity
- **GIVEN** a run display reference such as `task/r5`
- **WHEN** a client uses it
- **THEN** it is treated as a rendering of `(task_id, retry_index)`, not as a separately addressable identity


**Additional normative rules for this requirement:**

**Implementation status.** Run resources are currently addressed by the internal `run_id` rather than the task-scoped composite; responses carry `task_id`, `retry_index`, and `run_ref` so clients can recover the composite identity. Migration to task-scoped addressing is a Known Issue.

### Requirement: Readable Context Fields
The API MUST expose readable task and run context through separate fields so clients can render tables without decoding identifiers or display references.

#### Scenario: Client Table Rendering
- **GIVEN** task and run API responses
- **WHEN** a client renders an operator table
- **THEN** station, window, state, and identity data are available as distinct readable fields

### Requirement: Validated State-Change Operations
API operations that trigger state changes MUST be validated before durable side effects are committed. Retried operations MUST either be idempotent or report conflicts according to the idempotency rules of this specification.

#### Scenario: Validation Before Commit
- **GIVEN** a state-changing API request
- **WHEN** the request is invalid
- **THEN** no durable side effect is committed

### Requirement: Language-Neutral Contract
The API contract MUST remain language-neutral and MUST NOT require Go-specific behavior from clients, even though the reference implementation targets Linux clusters with shared POSIX storage and may be implemented in Go.

#### Scenario: Non-Go Client Interoperability
- **GIVEN** a client written in any language
- **WHEN** it implements this API contract
- **THEN** no implementation-language-specific behavior is required

### Requirement: Task Submission Endpoint

The API MUST provide a task-submission operation equivalent to `POST /tasks` (served as `POST /api/v1/tasks` in the reference implementation).
A submission request MUST include destination `station_id`, processing window start timestamp, and processing window end timestamp.

#### Scenario: Minimal Valid Submission
- **GIVEN** a request containing only `station_id`, window start, and window end
- **WHEN** the client submits it to the submission endpoint
- **THEN** the backend accepts it and creates a task

#### Scenario: Media Type Consistency
- **GIVEN** multiple supported request media types
- **WHEN** the same logical submission is sent in each media type
- **THEN** they map to the same canonical task submission semantics

**Additional normative rules for this requirement:**

It MAY include a client-supplied task identifier (if permitted by deployment policy), force flag, parent task identifier, parent run reference (`parent_task_id` plus parent retry index), split-group identifier (only through authorized workflows), priority, routing history, client metadata, and idempotency key.
The payload MAY be JSON, YAML, or another structured representation; supported media types MUST be documented, and all supported media types MUST map to the same canonical submission semantics.
### Requirement: Submission Destination Rules
The request MUST identify a destination station through `station_id`. Unknown station identifiers MUST be rejected before task creation (HTTP 400 or 404 per the error model).

#### Scenario: Unknown Station Rejected
- **GIVEN** a submission request with an unresolvable `station_id`
- **WHEN** the request is submitted
- **THEN** the backend rejects it with an unknown-station error and no task is created

### Requirement: Submission Window Rules
The processing window MUST include start and end timestamps with start less than or equal to end. The API SHOULD accept RFC 3339 timestamps with whole-second or fractional-second (including millisecond) precision, and compact UTC forms `YYYYMMDDTHHmmSS` and `YYYYMMDDTHHmmSSmmm`; compact forms without an explicit timezone are interpreted as UTC. Accepted timestamps SHOULD be normalized to a canonical UTC representation in responses.

#### Scenario: Millisecond RFC 3339 Accepted
- **GIVEN** a window start of `2025-07-03T11:12:39.000Z`
- **WHEN** the task is submitted
- **THEN** the request is accepted

#### Scenario: Compact UTC Accepted
- **GIVEN** a window timestamp of `20260513T131429` or `20260513T131429000`
- **WHEN** the task is submitted
- **THEN** it is interpreted as UTC and accepted

#### Scenario: Inverted Window Rejected
- **GIVEN** a window whose start is later than its end
- **WHEN** the task is submitted
- **THEN** the request is rejected as invalid and no task is created

### Requirement: Force And Parent Rules

The force flag MUST be persisted on the task and MUST default to `false` when omitted.
A forced run MAY create a new run, but successful forced runs MUST be non-canonical by default unless explicitly promoted by a documented operation or policy.

#### Scenario: Force Defaults To False
- **GIVEN** a submission without a force flag
- **WHEN** the task is created
- **THEN** the persisted force flag is `false`

#### Scenario: Forced Run Non-Canonical
- **GIVEN** a forced run that succeeds
- **WHEN** canonicality is decided
- **THEN** the run is non-canonical unless explicitly promoted by a documented operation or policy

#### Scenario: Bare Retry Index Parent Rejected
- **GIVEN** a submission with a parent run reference consisting only of a retry index or internal run key
- **WHEN** the request is validated
- **THEN** the reference is rejected as an invalid parent-run reference

#### Scenario: Split-Group Bypass Forbidden
- **GIVEN** an ordinary client task submission
- **WHEN** it includes split-group membership fields
- **THEN** the backend does not allow it to bypass split-group policy

**Additional normative rules for this requirement:**

Parent task and parent run references, when supplied, MUST refer to existing records and MUST be consistent with each other; a parent run reference MUST include the parent task identifier and task-scoped retry index — a bare retry index or internal run key is not a valid parent-run reference.
Backend-created downstream tasks MUST persist provenance links independently of readable routing history.
Ordinary client submissions MUST NOT be able to bypass split-group policy by supplying split-group membership fields.
### Requirement: Idempotency Key Semantics

Task submission MUST support an idempotency mechanism; the key MAY be supplied in a header, request field, or both, and the API MUST document the idempotency scope and retention window.
When the same key is reused with the same canonical request content, the API MUST return the same logical result — the status code may indicate the resource already existed (HTTP 200 on replay), but the body MUST identify the same task.

#### Scenario: Replay Returns Same Task
- **GIVEN** a successful submission with idempotency key K
- **WHEN** the identical request is retried with key K within the retention window
- **THEN** the response identifies the same task, and may return HTTP 200 indicating the resource already existed

#### Scenario: Conflicting Content Rejected
- **GIVEN** a previously used idempotency key K
- **WHEN** a request with key K but different canonical content is submitted
- **THEN** the backend returns an idempotency conflict (409) and no task is created or mutated

#### Scenario: Lost-Response Retry Safe
- **GIVEN** a submission that succeeded but whose response was lost
- **WHEN** the client retries with the same key within the retention window
- **THEN** the retry is safe and returns the same logical result

**Additional normative rules for this requirement:**

When the same key is reused with conflicting canonical request content, the API MUST reject the request with an idempotency conflict (HTTP 409) and MUST NOT create or mutate a task.
If a request succeeds but the client loses the response, retrying with the same key MUST be safe within the documented retention window.
### Requirement: Submission Validation Scope

Validation failures MUST NOT create tasks.
Validation MUST cover at least request shape, destination resolution, timestamp validity, parent references, split-group authorization where applicable, and schema validity of supplied routing content.
A duplicate request without an idempotency key MAY create a new task unless an additional documented deduplication policy applies.

#### Scenario: No Task On Validation Failure
- **GIVEN** a submission failing shape, destination, timestamp, parent, or routing-schema validation
- **WHEN** the backend responds
- **THEN** no task exists in the control plane

#### Scenario: Duplicate Without Key
- **GIVEN** two identical submissions without an idempotency key
- **WHEN** no additional deduplication policy is documented
- **THEN** the backend may create two distinct tasks

**Additional normative rules for this requirement:**

Submission idempotency deduplicates exact client retries; processing-fingerprint deduplication controls canonical execution and MUST NOT be replaced by submission idempotency.
### Requirement: Submission Response Semantics
A successful submission response MUST include the task identifier and current task summary state, and SHOULD include links or identifiers for retrieving task detail and related runs. If the task is accepted but no run exists yet, the response MUST make that clear. If a run is created synchronously, the response MAY include the initial run reference as `task_id`, `retry_index`, and optionally `run_ref`, but clients MUST still treat run creation and dispatch as separate lifecycle state.

#### Scenario: Accepted Without Run
- **GIVEN** a submitted task that has no run yet
- **WHEN** the submission response is returned
- **THEN** the response makes clear the task is accepted but has no run

#### Scenario: Synchronous Run Reference
- **GIVEN** a submission where a run is created synchronously
- **WHEN** the response is returned
- **THEN** it may include the initial run reference expressed as task-scoped identity

### Requirement: Get Task By Identifier

The API MUST provide an operation equivalent to `GET /tasks/{task_id}` returning task identity, destination, processing window, force flag, current state, parent references when present, split-group reference when present, latest run when present, canonical run when present, timestamps, and links or embedded summaries per representation policy.

#### Scenario: Task Detail Retrieved
- **GIVEN** an existing task
- **WHEN** `GET /api/v1/tasks/{task_id}` is called
- **THEN** the response includes destination, window, force flag, state, and canonical/latest run references when present

#### Scenario: Waiting Inputs State
- **GIVEN** a join-created downstream task whose mandatory inputs are not yet resolved
- **WHEN** the task is retrieved
- **THEN** its state is reported as `waiting_inputs` and this is treated as non-terminal

#### Scenario: Unknown Task Not Found
- **GIVEN** a request for a nonexistent task
- **WHEN** the endpoint is called
- **THEN** a not-found error (404) is returned

**Additional normative rules for this requirement:**

`waiting_inputs` SHALL be a valid non-terminal task state for join-created downstream tasks with no run yet because mandatory input resolution is incomplete.
If the task does not exist, the API MUST return a not-found error.
### Requirement: List Or Query Tasks
The API MUST provide an operation equivalent to `GET /tasks` supporting filters for destination station, task state, parent task, split group, and force flag, with pagination and deterministic documented ordering (creation timestamp descending by default with a stable tie-breaker such as task identifier).

#### Scenario: Filter By Station And State
- **GIVEN** tasks for multiple stations and states
- **WHEN** the list endpoint is called with station and state filters
- **THEN** only matching tasks are returned

#### Scenario: Deterministic Ordering
- **GIVEN** multiple tasks
- **WHEN** the list endpoint is called
- **THEN** results are ordered by creation timestamp descending with a documented stable tie-breaker


**Additional normative rules for this requirement:**

**Implementation status.** The implemented list supports `station_id`, `state`, `parent_task_id`, `split_group_id`, `force`, `limit`, and `cursor` (the `missing` filter for tasks whose runs lack a given state is available through the CLI/console path). Window overlap/containment, creation-timestamp range, parent-run reference, and canonical-run-presence filters are not implemented.

### Requirement: Get Run
The API MUST provide an operation equivalent to `GET /runs/{run_id}`.

#### Scenario: Run Detail Retrieved
- **GIVEN** an existing run
- **WHEN** the run endpoint is called
- **THEN** the response includes state, `internal_state`, canonical classification, active job, timestamps, and failure reason when present

#### Scenario: Working Root Opaque
- **GIVEN** a run detail response containing a working-root path
- **WHEN** a client inspects it
- **THEN** the path is treated as opaque and no identity or state is inferred from its components

#### Scenario: Unknown Run Not Found
- **GIVEN** a request for a nonexistent run
- **WHEN** the run endpoint is called
- **THEN** a not-found error (404) is returned


**Additional normative rules for this requirement:**

**Implementation status.** The current API addresses runs by internal unique `run_id` (`GET /api/v1/runs/{run_id}`); the task-scoped form `GET /tasks/{task_id}/runs/{retry_index}` is the preferred target model and is not yet implemented. The response MUST identify station revision, state (a simplified external `state` alongside the diagnostic `internal_state`), canonical classification, working-root path subject to access policy, processing fingerprint or reference subject to access policy, job-order artifact when present, manifest reference, active job, timestamps, failure reason when present, and finalization summary when present. It MAY surface `executor_type` and `execution_node` as convenience denormalizations from the active job. The working-root path is an opaque operator-visible path; clients MUST NOT infer identity or control-plane state from its structure. Unknown runs MUST return a not-found error.

### Requirement: List Or Query Runs
The API MUST provide an operation equivalent to `GET /runs` and SHOULD provide `GET /tasks/{task_id}/runs` for task-scoped listing. Supported filters SHOULD include task identifier, station identifier, run state, and canonical classification. The API MUST support pagination with deterministic ordering (creation timestamp descending by default, tie-broken by `(task_id, retry_index)`). Run summaries SHOULD include `executor_type` when known.

#### Scenario: Filter Runs By Task
- **GIVEN** runs belonging to multiple tasks
- **WHEN** runs are listed filtered by task identifier
- **THEN** only that task's runs are returned

#### Scenario: Executor Type In Summary
- **GIVEN** a run with a known executor
- **WHEN** run summaries are listed
- **THEN** `executor_type` appears in the summary


**Additional normative rules for this requirement:**

**Implementation status.** The implemented list supports filtering by task, station, and state. The `retry_index`, station-revision, canonical-classification, fingerprint, terminal-timestamp-range, and scheduler-identifier filters are not implemented; retry and canonicality classifications are exposed on run detail instead.

### Requirement: Get Jobs For A Run

The API SHOULD provide an operation equivalent to `GET /runs/{run_id}/jobs`; a standalone `GET /jobs/{job_id}` is implemented.
Job responses MUST expose run reference, executor type, scheduler identifier when known, scheduler-native state, execution node when known, submission timestamp, last poll timestamp (`last_observed_at`), cancellation timestamps when present, terminal scheduler timestamp when present, and reconciliation status when relevant.

#### Scenario: Job Listing Includes Observation Time
- **GIVEN** jobs for a run
- **WHEN** the jobs endpoint is called
- **THEN** each job includes `last_observed_at` so scheduler-state staleness can be judged

#### Scenario: Standalone Job Lookup
- **GIVEN** an existing job identifier
- **WHEN** `GET /api/v1/jobs/{job_id}` is called
- **THEN** the job record is returned with scheduler-native state and timestamps

**Additional normative rules for this requirement:**

Scheduler-native state MAY be stale; the response MUST include enough timestamp metadata for clients to understand when scheduler state was last observed.
### Requirement: Artifacts Content And Metadata

The API MUST provide an operation equivalent to `GET /runs/{run_id}/artifacts`.
Artifact content is exposed through `GET /artifacts/{artifact_id}/content` with a checksum header; directory artifacts MUST be rejected for content access.

#### Scenario: Artifact Content With Checksum
- **GIVEN** a regular-file artifact
- **WHEN** `GET /artifacts/{artifact_id}/content` is called
- **THEN** the content is returned with a checksum header

#### Scenario: Directory Content Rejected
- **GIVEN** a directory artifact
- **WHEN** content access is requested
- **THEN** the request is rejected

#### Scenario: Metadata Survives Content Loss
- **GIVEN** an artifact whose bytes were removed by retention
- **WHEN** metadata is queried
- **THEN** metadata succeeds and availability is reported as unavailable rather than not-found

#### Scenario: Audit Artifact Types Distinguishable
- **GIVEN** job orders, manifests, logs, outputs, and `task-out.yaml` files for a run
- **WHEN** artifacts are listed
- **THEN** each is distinguishable by logical artifact type

**Additional normative rules for this requirement:**

Artifact metadata MUST be queryable independently of content access and MUST identify the artifact, producing run, logical type, object kind, path or access reference subject to policy, size when available and meaningful, checksum, validation status, availability, and publication summary when available.
Regular-file download may be direct streaming, ranged content, pre-signed references, or deployment-specific handles; directory content access is not implied by metadata availability and MUST be documented if supported.
Supported object kinds MUST include at least regular file and directory.
The `logical_type` filter SHOULD be supported.
Job orders, manifest exports, logs, output objects, and `task-out.yaml` files MUST be distinguishable by logical artifact type, and the job order, exported manifest, and `task-out.yaml` MUST be exposed as audit artifacts when permitted by access policy.
If artifact bytes are unavailable due to retention, deletion, or storage failure, metadata queries SHOULD still succeed and MUST report unavailable content distinctly from missing artifact metadata.


**Implementation status.** Only the `logical_type` filter is implemented; file type, validation status, canonical-only, latest-only, and publication-status filters are absent, and artifact listing is unpaginated. A task-scoped artifact listing (`GET /tasks/{task_id}/artifacts`) is not implemented.
### Requirement: Log Access
The API MUST provide a way to retrieve log metadata and SHOULD provide paged or streaming access to log content. The implementation exposes run log files as artifacts of logical type `log`, retrievable through the run artifact listing and the artifact content endpoint. Log content access MUST remain attributable to the producing run. If logs may be large, the API SHOULD avoid requiring clients to retrieve the whole log in one response.

#### Scenario: Run Logs As Artifacts
- **GIVEN** a run that produced logs
- **WHEN** the run's artifacts are listed
- **THEN** log files appear with logical type `log` and are attributable to the producing run

### Requirement: Split Group Endpoints
The API MUST provide an operation equivalent to `GET /groups/{group_id}`.

#### Scenario: Group Detail Retrieved
- **GIVEN** an existing split group
- **WHEN** the group endpoint is called
- **THEN** the response includes closure state, readiness state, grouped evaluation state, member counts, and aggregation task when materialized

#### Scenario: No Membership Inference
- **GIVEN** child tasks that share a station or time range with a group
- **WHEN** a client determines group membership
- **THEN** membership is determined only from group records per `group_id`, never inferred from station, filename, archive, or time overlap

#### Scenario: Close Group
- **GIVEN** an open split group
- **WHEN** `POST /api/v1/groups/{group_id}/close` is called
- **THEN** the group's closure state is recorded


**Additional normative rules for this requirement:**

**Implementation status.** The implemented endpoints are `GET /api/v1/groups`, `GET /api/v1/groups/{group_id}`, and `POST /api/v1/groups/{group_id}/close`, using the `groups` resource path rather than `split-groups`. The paginated member-inspection sub-resource is not implemented. Split-group responses MUST expose group identifier, parent task or run reference, parent window, group evaluation mode, grouped evaluation target, membership basis, closure state, readiness state, grouped evaluation state, grouped-evaluation window override when present, aggregation target when present, aggregation task identifier when materialized, member counts by membership and contribution state, timestamps, and failure or timeout reason when present; they SHOULD expose paginated member details or a link to them, including child task identifier, child key, expected child window when present, membership state, canonical contributing run when available, selected contributing artifacts when available, exclusion status, and readiness contribution state. Readiness and aggregation MUST be evaluated per `group_id`; clients MUST NOT infer group membership from station identity, product type, filename pattern, archive location, overlapping time range, or routing-history text.

### Requirement: Provenance Query Availability

Provenance queries MUST expose machine-queryable links between tasks, runs, artifacts, split groups, and aggregation results such that clients can determine: which run produced an artifact; which resolved inputs were consumed by a run when those inputs reference prior VeriProc artifacts; which producing run created a downstream task; which tasks belong to a split group; and which aggregation task and run consumed selected child results.

#### Scenario: Artifact Producer Query
- **GIVEN** a machine-queryable provenance API
- **WHEN** a client asks which run produced an artifact
- **THEN** the producing run is determined from provenance links, not from readable history text

**Additional normative rules for this requirement:**

Readable task history may be returned for human inspection but MUST NOT be the only API representation of provenance.


**Implementation status.** No relationship-provenance query endpoint is implemented; provenance is persisted relationally and exposed through task-detail projections such as routing history. A dedicated `GET /tasks/{task_id}/relationships` is not available (Known Issue).
### Requirement: Retry And Promote Endpoints
The implementation exposes `POST /tasks/{task_id}/retry`, which MUST create a new run attempt with a new `retry_index` for the task, and `POST /runs/{run_id}/promote`, which MUST explicitly promote a completed non-canonical run to canonical ownership of its processing fingerprint, subject to a reconciliation guard.

#### Scenario: Task Retry Creates New Attempt
- **GIVEN** a task with an existing run
- **WHEN** `POST /api/v1/tasks/{task_id}/retry` is called
- **THEN** a new run is created with the next `retry_index`

#### Scenario: Promote Non-Canonical Run
- **GIVEN** a completed non-canonical run
- **WHEN** `POST /api/v1/runs/{run_id}/promote` is called and no reconciliation guard blocks it
- **THEN** the run becomes the canonical owner of its processing fingerprint

### Requirement: Cancellation Request Semantics

Cancellation is a request to stop or prevent further execution of a run and is NOT a deletion operation.
The implementation exposes `POST /runs/{run_id}/cancel`, which MUST distinguish accepted, already-terminal, and completed outcomes and MUST never delete filesystem state.

#### Scenario: Cancellation Accepted
- **GIVEN** a run in a cancellable state with an active scheduler job on a cancellation-capable executor
- **WHEN** `POST /api/v1/runs/{run_id}/cancel` is called
- **THEN** the backend requests scheduler cancellation, persists cancellation-request state, and the response indicates the request was accepted (not completed)

#### Scenario: Cancellation Unsupported
- **GIVEN** a run on an executor or station that does not support cancellation
- **WHEN** cancellation is requested
- **THEN** the API returns cancellation-unsupported (409 or 501) and the run is not marked cancelled

#### Scenario: Already Terminal Run
- **GIVEN** a run already in a terminal state
- **WHEN** cancellation is requested
- **THEN** the response reports the current terminal state with no new cancellation side effect

#### Scenario: Cancellation State Visible
- **GIVEN** an accepted cancellation request
- **WHEN** the run is subsequently queried
- **THEN** cancellation-request state is exposed until a terminal or reconciliation state is reached

#### Scenario: No Deletion On Cancel
- **GIVEN** a cancelled run
- **WHEN** cancellation completes
- **THEN** working roots, artifacts, logs, jobs, and provenance records remain untouched

**Additional normative rules for this requirement:**

Cancellation MAY be allowed only when the run is in a cancellable state (such as `pending`, `preparing`, `dispatching`, `dispatched`, or `running`) per executor and station policy; cancellation of `finalizing` runs MAY be allowed only if consistency of validation, publication, deduplication, and downstream effects can be preserved.
If the executor supports cancellation and the run has an active scheduler job, the backend MUST request scheduler cancellation and persist cancellation-request state, and the response MUST distinguish cancellation request accepted from cancellation completed.
If the executor does not support cancellation or station policy forbids it, the API MUST return cancellation-unsupported or invalid-state-transition and MUST NOT mark the run cancelled.
If the target run is already terminal, the API SHOULD return the current terminal state and MUST NOT create a new cancellation side effect.
After acceptance, subsequent run queries MUST expose cancellation-request state until the run reaches `cancelled`, `failed`, `complete`, `orphaned`, or another documented terminal or reconciliation state; a run may fail or complete despite a cancellation request if execution had already passed the cancellation point, and the final state MUST be persisted and queryable.
Cancellation MUST NOT remove working roots, artifacts, logs, jobs, or provenance records; cleanup is governed by retention policy.
### Requirement: Station Status And Summary Endpoints

The rules in this requirement are normative and MUST be satisfied by the implementation.
The API SHOULD expose station status and pause control for operator tooling (required when the operator web console is supported).

#### Scenario: Station Listing
- **GIVEN** configured stations
- **WHEN** `GET /api/v1/stations` is called
- **THEN** each entry includes identity, display name when known, and paused state

#### Scenario: Station Summary Contents
- **GIVEN** a station with activity in the requested interval
- **WHEN** the station summary endpoint is called with `since`
- **THEN** running and queued counts, success and failure counters, non-empty slot occupants, and an authoritative refresh timestamp are returned

#### Scenario: Slot Occupant Identity
- **GIVEN** slot occupants in a station summary
- **WHEN** a client identifies a run
- **THEN** it uses `task_id` and `retry_index`, not the internal `run_id`

**Additional normative rules for this requirement:**

The reference implementation exposes `GET /api/v1/stations`, `GET /api/v1/stations/summary?since=...`, `GET /api/v1/stations/{station_id}/summary?since=...`, `POST /api/v1/stations/{station_id}/pause`, `POST /api/v1/stations/{station_id}/unpause`, and `GET /api/v1/tasks/{task_id}/runs`.
Station list responses MUST include station identity, display name when known, and paused state.
Station summary responses MUST include station identity, paused state, running and queued counts, success and failure counters for the caller-supplied interval, visible non-empty slot occupants, and an authoritative refresh timestamp.
Slot occupants MUST include stable task-scoped run identity through `task_id` and `retry_index`; implementations MAY additionally include an internal `run_id` for compatibility, but clients MUST NOT treat it as the conceptual run identity.
### Requirement: Station Pause And Unpause

Pause and unpause operations MUST validate that the station exists, persist the new paused state, and return the resulting station status.
A paused station MUST NOT dispatch ready runs for that station; the backend MAY continue to admit accepted tasks and prepare ready runs while paused so queued work remains visible.

#### Scenario: Pause Persists And Blocks Dispatch
- **GIVEN** a station with ready runs
- **WHEN** pause is called and persisted
- **THEN** ready runs for that station are not dispatched, while the backend may still admit tasks and prepare runs

#### Scenario: Reader Cannot Pause
- **GIVEN** API authentication enabled and a reader-scoped credential
- **WHEN** the reader calls pause or unpause
- **THEN** the request is rejected as unauthorized

#### Scenario: Unknown Station Pause
- **GIVEN** a pause request for a nonexistent station
- **WHEN** the operation is attempted
- **THEN** it is rejected because the station does not exist

**Additional normative rules for this requirement:**

Mutating station operations REQUIRE operator authorization when API authentication is enabled; reader credentials may use list and summary endpoints but MUST NOT pause or unpause stations.
### Requirement: Task Deletion Cascade

The API MUST provide destructive, irreversible deletion operations, exposed as `DELETE /api/v1/tasks/{task_id}` and `DELETE /api/v1/runs/{run_id}`.

#### Scenario: Full Task Cascade
- **GIVEN** a task with descendant tasks, runs, jobs, artifacts, and publications
- **WHEN** `DELETE /api/v1/tasks/{task_id}` is called with default cascade
- **THEN** the task, all descendants, all dependent rows, and affected working roots are removed

#### Scenario: Cascade False Skips Descendants
- **GIVEN** a task with descendant tasks
- **WHEN** deletion is requested with `cascade=false`
- **THEN** descendant-task cascade is skipped while the target task and its own runs are handled

#### Scenario: Run Deletion Scoped
- **GIVEN** a task with multiple runs
- **WHEN** `DELETE /api/v1/runs/{run_id}` is called
- **THEN** only that run, its dependent rows, and its working root are removed; the task and sibling runs remain

**Additional normative rules for this requirement:**

Task deletion MUST remove the task, every descendant task linked through `parent_task_id`, all runs of those tasks, and every dependent control-plane row (jobs, artifacts, rolling-archive publications, resolved input manifests and entries, deduplication records, canonicality audit rows, split-group memberships, task history entries, provenance links, and idempotency records), plus each affected run's on-disk working root.
Run deletion MUST remove a single run with its dependent rows and working root, leaving the owning task and sibling runs untouched.


**Implementation status.** The delete endpoints additionally accept `?cascade=true/false` (and `--quiet` in the CLI). When `cascade` is explicitly false, the descendant-task cascade is skipped, relaxing the unconditional cascade rule; the default remains full cascade.
### Requirement: Deletion Dry Run And Report

Both deletion endpoints MUST accept an optional `dry_run` query parameter (`?dry_run=true`); in dry-run mode the implementation MUST NOT delete anything and MUST return a report describing what would be removed.
Both endpoints MUST return a deletion report containing per-table deleted-row counts, the affected task and run identifiers, the working roots that were (or would be) removed, the count of working roots actually removed, and any filesystem errors.

#### Scenario: Dry Run Deletes Nothing
- **GIVEN** a task selected for deletion with `dry_run=true`
- **WHEN** the request executes
- **THEN** no state is deleted and the report describes what would be removed

#### Scenario: Deletion Report Contents
- **GIVEN** a completed (non-dry-run) deletion
- **WHEN** the response is returned
- **THEN** it contains per-table deleted-row counts, affected identifiers, removed working roots, removal count, and filesystem errors

**Additional normative rules for this requirement:**

Unknown tasks or runs MUST return a not-found error.
### Requirement: Deletion Transactional And Filesystem Semantics
Database deletion MUST be transactional so a failure leaves the control plane consistent. Filesystem working-root removal occurs AFTER the database transaction commits; working roots that cannot be removed MUST be reported without failing the request, so the operator can follow up manually.

#### Scenario: DB Failure Leaves Consistency
- **GIVEN** a deletion that fails mid-transaction
- **WHEN** the database transaction aborts
- **THEN** the control plane remains consistent

#### Scenario: Unremovable Working Root Reported
- **GIVEN** a working root that cannot be removed after the transaction commits
- **WHEN** the deletion response is built
- **THEN** the failure is reported in the response and the request does not fail

### Requirement: Maintenance Clean Endpoint
The API MUST provide a time-bounded bulk deletion operation at `POST /api/v1/maintenance/clean` for retention and housekeeping. The request body MAY include `before`, `after`, `basis`, and `dry_run`. The `basis` field selects task timestamps for comparison:

#### Scenario: Processing-Time Basis Default
- **GIVEN** a clean request with `before` and no `basis`
- **WHEN** the request executes
- **THEN** tasks with `created_at <= before` are selected

#### Scenario: Processing-Window Basis
- **GIVEN** a clean request with `basis=processing-window` and `before`
- **WHEN** the request executes
- **THEN** tasks with `window_end <= before` are selected

#### Scenario: Range Selection
- **GIVEN** a clean request with both `after` and `before`
- **WHEN** the request executes
- **THEN** only tasks within `[after, before]` on the selected basis are selected

#### Scenario: Invalid Basis Rejected
- **GIVEN** a clean request with an unrecognized `basis`
- **WHEN** the request is validated
- **THEN** an invalid-request error is returned

#### Scenario: No Cutoffs Rejected
- **GIVEN** a clean request with neither `before` nor `after`
- **WHEN** the request is validated
- **THEN** an invalid-request error is returned

#### Scenario: Clean Dry Run
- **GIVEN** a clean request with `dry_run=true`
- **WHEN** the request executes
- **THEN** nothing is deleted and the deletion report describes the would-be removal


**Additional normative rules for this requirement:**

- `processing-time` (default when `basis` is omitted) compares against `created_at`: `before` selects tasks with `created_at <= before`; `after` selects tasks with `created_at >= after`.
- `processing-window` compares against the sensing window: `before` selects tasks with `window_end <= before`; `after` selects tasks with `window_start >= after`.

When both `before` and `after` are supplied they combine with AND, selecting tasks within `[after, before]`. An unrecognized `basis` value MUST return an invalid-request error. Timestamps MUST accept RFC 3339 and compact UTC forms, consistent with task submission. At least one of `before` or `after` MUST be supplied; a request with neither MUST return an invalid-request error. For each selected task, the clean operation applies the same cascade and filesystem semantics as task deletion and returns the same deletion report; with `dry_run` true, nothing is deleted. Clean and maintenance/deletion operations are mutating and REQUIRE operator authorization when API authentication is enabled.

### Requirement: Task Summary Representation

A task summary MUST include `task_id`, destination `station_id`, processing window, force flag, task state, creation timestamp, latest run reference when present (at minimum `latest_retry_index`, optionally `latest_run_ref`), and canonical run reference when present (at minimum `canonical_retry_index`, optionally `canonical_run_ref`).
Destination and window fields are authoritative task fields, not alternate identities.

#### Scenario: Summary Contains Required Fields
- **GIVEN** any task summary response
- **WHEN** the client inspects it
- **THEN** identity, destination, window, force, state, and creation timestamp are present

**Additional normative rules for this requirement:**

A summary MAY include parent references, split-group identifier, priority, failure summary, and links.
### Requirement: Task Detail Representation
Task detail MUST include all task summary fields plus immutable routing content, parent references when present, readable routing history when available, provenance-query link or relationship summary, client metadata when access policy permits, and related run summaries or links. Task detail MUST distinguish backend-maintained mutable status from immutable routing content.

#### Scenario: Mutable Versus Immutable Separation
- **GIVEN** a task detail response
- **WHEN** a client distinguishes routing content from status
- **THEN** the representation makes clear which fields are immutable routing content and which are backend-maintained status


**Additional normative rules for this requirement:**

**Implementation status.** The implemented task detail carries summary fields, parent and split-group references, latest/canonical run references, and state, but does not embed routing history, client metadata, a routing-content block, or related run summaries.

### Requirement: Run Summary Representation

A run summary MUST include `task_id`, `retry_index`, optional `run_ref`, station identity and station revision identifier, task processing window start and end (embedded or via an included task summary), run state, canonical classification, creation timestamp, and terminal timestamp when present.

#### Scenario: Run Summary Identity
- **GIVEN** a run summary
- **WHEN** the client identifies the run
- **THEN** `task_id` and `retry_index` are the identity, and window/station fields are recognized as projections

**Additional normative rules for this requirement:**

It MAY include scheduler identifier, active job identifier, working-root path subject to access policy, failure reason or a same-value `failure_summary` alias, and links.
Station and task-window fields MAY be projections or denormalized query aids but MUST be derived from authoritative task and station-revision records and MUST NOT be treated as alternate identities.
### Requirement: Run Detail Representation

Run detail MUST include all run summary fields plus working-root path subject to access policy, job-order artifact identifier when present, manifest identifier when present, processing fingerprint or reference subject to access policy, instance configuration revision when available, generator identities when available, active job, job summaries, finalization summary, failure reason, and lifecycle timestamps.

#### Scenario: Internal State Diagnostics
- **GIVEN** a run in a reconciliation or finalization phase
- **WHEN** the run detail is retrieved
- **THEN** the simplified `state` is accompanied by `internal_state` or equivalent diagnostics distinguishing the phase

**Additional normative rules for this requirement:**

Internal reconciliation or orphan states MAY be exposed directly if documented; if internal states are mapped to a simplified public state model, the API MUST still expose enough diagnostic information (via `internal_state`) for operators to distinguish active execution, finalization, failure, cancellation, and reconciliation in progress.
### Requirement: Job Summary And Detail Representation

A job summary MUST include job identifier, task-scoped run reference (`task_id`, `retry_index`), executor type, scheduler identifier when known, scheduler-native state, submission timestamp, and last-observed timestamp when available.
For SLURM jobs, the executor type SHOULD use the canonical distributed mode (such as `slurm-native` or `slurm-docker`) unless executor family and runtime mode are exposed separately.

#### Scenario: SLURM Executor Mode Naming
- **GIVEN** a SLURM job
- **WHEN** its executor type is reported
- **THEN** it uses the canonical distributed mode name such as `slurm-native` or `slurm-docker`

**Additional normative rules for this requirement:**

Job detail SHOULD include submission attempt number, queue or partition, cancellation fields, terminal scheduler timestamp, reconciliation status, and scheduler-native diagnostic metadata when available; for SLURM it SHOULD also expose effective account, QoS, resource request, node list, elapsed time, scheduler exit code, and wrapper log references when available and permitted.
### Requirement: Artifact Representation

An artifact representation MUST include artifact identifier, producing task-scoped run reference or external origin, logical artifact type, object kind, path or access reference subject to access policy, creation or discovery timestamp, and availability status.

#### Scenario: Directory Artifact Without Checksum
- **GIVEN** a directory artifact
- **WHEN** its representation is returned
- **THEN** checksum fields may be absent or null while object kind is directory

**Additional normative rules for this requirement:**

It SHOULD include file type, size when available and meaningful, checksum value, checksum algorithm, checksum source, validation status, validation summary, media type, and publication summary when available.
Checksum fields MAY be absent or null under the default `available_only` integrity policy and for directory artifacts.
### Requirement: Canonical And Non-Canonical Visibility
Run representations MUST expose canonical classification explicitly so a client can distinguish: canonical completed run; completed non-canonical forced rerun; completed duplicate or superseded run; run not yet eligible for canonical classification; and failed, cancelled, or orphaned run. Task representations MUST expose canonical run identity when one exists using the task-scoped run reference; if no canonical run exists, task detail MUST NOT imply that the latest run is canonical.

#### Scenario: Forced Rerun Distinguishable
- **GIVEN** a completed forced rerun and the canonical run of the same fingerprint
- **WHEN** run representations are inspected
- **THEN** the client can distinguish canonical from completed non-canonical forced rerun

#### Scenario: No False Canonical Implication
- **GIVEN** a task whose latest run is not canonical
- **WHEN** task detail is retrieved
- **THEN** the representation does not imply the latest run is canonical

### Requirement: Consistency Semantics Observed State

Database-backed reads MUST reflect committed control-plane state at minimum; stronger guarantees MUST be documented.
Scheduler-derived fields are observations and MAY lag; the API MUST expose last-observed timestamps or equivalent metadata when returning scheduler-native state.

#### Scenario: Scheduler State Staleness Visible
- **GIVEN** a run with a scheduler job
- **WHEN** scheduler-native state is returned
- **THEN** the response includes the last-observed timestamp so staleness is assessable

#### Scenario: Scheduler Success Is Not Completion
- **GIVEN** a run whose scheduler job succeeded but whose outputs are not yet validated and published
- **WHEN** run state is queried
- **THEN** the run is not reported as complete

**Additional normative rules for this requirement:**

Filesystem-derived fields (availability, byte size, checksum metadata, log tail) MAY lag behind database state; the API MUST distinguish persisted artifact metadata from live file accessibility when they can differ.
A run MUST NOT be reported as complete solely because a scheduler job succeeded — completion requires output validation, artifact persistence, canonicality decision, and downstream reconciliation according to policy.
### Requirement: Latest Canonical Active Job Determination
`latest_run` for a task MUST be determined by the backend's documented run ordering, normally creation timestamp or retry index with a stable tie-breaker. `canonical_run` MUST be determined by run canonical classification and deduplication ownership, NOT by scheduler success or recency. Both MUST be represented as task-scoped run references. `active_job` MUST refer to the non-terminal job currently associated with a task-scoped run under executor policy.

#### Scenario: Canonical Not Latest
- **GIVEN** a task whose canonical run is an earlier retry and whose latest run is a non-canonical rerun
- **WHEN** task detail is retrieved
- **THEN** canonical and latest run references point to different runs, with canonicality decided by classification not recency

### Requirement: Paginated List Envelope

Paginated list responses MUST include list items, page size or limit, a next-page token or equivalent continuation value when more results are available, applied ordering, and applied filters or a request echo when practical.
Continuation tokens, if used, SHOULD be opaque to clients.
Paginated endpoints MUST include a documented page-size limit and deterministic ordering with an indicator of whether more results are available.

#### Scenario: Continuation Via Opaque Cursor
- **GIVEN** more results than one page
- **WHEN** a list endpoint returns a page
- **THEN** the response includes `next_cursor` usable to fetch the next page

#### Scenario: Filter Validation Error
- **GIVEN** an invalid filter or pagination parameter
- **WHEN** a list endpoint is called
- **THEN** a stable error response body is returned

**Additional normative rules for this requirement:**

The envelope MAY include total counts if provided efficiently and consistently.


**Implementation status.** Run, task, and console-gateway list endpoints implement cursor pagination with `next_cursor`. The run-list envelope additionally echoes `page_size`, `ordering`, and `filters`; the task-list envelope currently returns only `next_cursor`.
### Requirement: Additional Resources Bounded
The API MAY expose additional resources for instance configuration, generation-hook identities, rolling archive definitions, idempotency records, or schema metadata, but such resources MUST NOT allow clients to bypass validation or mutate control-plane state outside documented administrative operations.

#### Scenario: No Bypass Through Auxiliary Resources
- **GIVEN** an auxiliary API resource
- **WHEN** a client attempts to mutate control-plane state through it
- **THEN** the attempt is rejected unless via a documented administrative operation

### Requirement: Error Envelope Fields

The API MUST use a stable error model.
HTTP status codes may follow REST conventions, but clients MUST rely on machine-readable error codes for programmatic handling.
Error response bodies MUST include `error.code` (stable machine-readable code), `error.message` (human-readable), and a request correlation identifier when available.

#### Scenario: Machine-Readable Error Code
- **GIVEN** any API error
- **WHEN** the error body is inspected
- **THEN** `error.code` and `error.message` are present and the code is stable across the API version

#### Scenario: Idempotency Conflict Details
- **GIVEN** an idempotency-conflict error
- **WHEN** the error body is returned
- **THEN** it includes the conflict code (409) and may include the conflicting resource identifier

**Additional normative rules for this requirement:**

They SHOULD include field-level validation details (`fields`) when applicable, a retryability indicator (`retryable`) when useful, the dependency name for dependency failures when safe to expose, and the conflicting resource identifier (`conflicting_resource_id`) for idempotency conflicts when safe to expose.
The API MUST NOT expose sensitive scheduler, filesystem, or configuration details beyond deployment access policy.
### Requirement: Invalid Request Errors
Invalid-request errors SHALL return HTTP 400 when request shape, field values, filters, pagination parameters, or timestamp values are invalid.

#### Scenario: Malformed Submission Rejected
- **GIVEN** a submission with invalid field values or timestamps
- **WHEN** the request is submitted
- **THEN** the API responds 400 with an invalid-request error code

### Requirement: Unknown Station Errors
Unknown-station errors SHALL return HTTP 400 or 404 when the destination `station_id` cannot be resolved.

#### Scenario: Unresolvable Destination
- **GIVEN** a submission to a station that does not exist
- **WHEN** it is submitted
- **THEN** the API responds 400 or 404 with an unknown-station error code

### Requirement: Resource Not Found Errors
Resource-not-found errors SHALL return HTTP 404 when the requested task, run, job, artifact, split group, or relationship does not exist or is not visible.

#### Scenario: Missing Artifact
- **GIVEN** a content or metadata request for a nonexistent artifact
- **WHEN** the request is made
- **THEN** the API responds 404 with a not-found error code

### Requirement: Invalid State Transition Errors
Invalid-state-transition errors SHALL return HTTP 409 when the requested operation is not valid for the current resource state.

#### Scenario: Transition From Terminal State
- **GIVEN** an operation that is invalid for the resource's current state
- **WHEN** the operation is requested
- **THEN** the API responds 409 with an invalid-state-transition error code

### Requirement: Cancellation Unsupported Errors
Cancellation-unsupported errors SHALL return HTTP 409 or 501 when the executor, station policy, or deployment does not support cancellation for the target run.

#### Scenario: Unsupported Executor Cancellation
- **GIVEN** a target run on a non-cancellable executor
- **WHEN** cancellation is requested
- **THEN** the API responds 409 or 501 with a cancellation-unsupported error code

### Requirement: Reconciliation In Progress Errors
Reconciliation-in-progress errors SHALL return HTTP 409 or 423 when scheduler submission or run ownership is uncertain and requires reconciliation before the operation can proceed.

#### Scenario: Promote During Reconciliation
- **GIVEN** a promote request for a run whose ownership is under reconciliation
- **WHEN** the request is made
- **THEN** the API responds 409 or 423 with a reconciliation-in-progress error code

### Requirement: Dependency Unavailable Errors
Dependency-unavailable errors SHALL return HTTP 503 when the database, scheduler, shared storage, station registry, or rolling-archive dependency is unavailable, including the dependency name in the error body when safe to expose.

#### Scenario: Database Unavailable
- **GIVEN** an unreachable database dependency
- **WHEN** a request requiring it is made
- **THEN** the API responds 503 with a dependency-unavailable error code

### Requirement: Internal Error Handling
Internal errors SHALL return HTTP 500 for unexpected backend failures, without exposing sensitive details beyond deployment access policy.

#### Scenario: Unexpected Failure
- **GIVEN** an unexpected backend failure
- **WHEN** a request triggers it
- **THEN** the API responds 500 with a stable internal-error code and no sensitive detail leakage

### Requirement: Stable Field Names And Compatibility

The API MUST use stable field names and preserve identity distinctions within a version.
Backward-compatible changes may include adding optional response fields, new filter parameters, new enum values under an unknown-value handling policy, and new resource links.
The API MUST NOT remove required fields, change field meanings, change identity semantics, or reinterpret lifecycle states within a version without a documented compatibility break.

#### Scenario: Unknown Field Tolerance
- **GIVEN** a response containing a field the client does not know
- **WHEN** the client processes the response
- **THEN** the client ignores the unknown field without failing

#### Scenario: Absence Is Not False
- **GIVEN** an omitted optional boolean field
- **WHEN** a client interprets it
- **THEN** it does not treat absence as equivalent to false unless the field definition says so

**Additional normative rules for this requirement:**

Clients SHOULD ignore unknown fields and MUST NOT assume absence of an optional field has the same meaning as a false value unless the field definition says so.
### Requirement: Health And Readiness Endpoints
Implementations SHOULD expose health and readiness endpoints for operations. A health endpoint MAY report process liveness. A readiness endpoint SHOULD report whether required dependencies (database, scheduler adapter, station configuration source, shared storage, rolling-archive configuration) are usable for control-plane operations, and MUST return HTTP 503 when the backend is not ready. Metrics endpoints may be deployment-specific and MUST NOT be required for client-workflow correctness.

#### Scenario: Readiness Reports 503 When Unready
- **GIVEN** a backend whose required dependency is unusable
- **WHEN** the readiness endpoint is called
- **THEN** HTTP 503 is returned with dependency state

#### Scenario: Ready Backend
- **GIVEN** a backend with all required dependencies usable
- **WHEN** the readiness endpoint is called
- **THEN** it reports readiness for control-plane operations


**Additional normative rules for this requirement:**

**Implementation status.** Backend and API version information is exposed through the health/readiness endpoints.
