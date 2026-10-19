# CLI Specification

## Purpose

 Defines the normative behavior of the VeriProc command-line interface (CLI), an operator, automation, and scripting client layered strictly on the VeriProc REST API. The CLI must not bypass the API to access backend state, must preserve all control-plane identity distinctions, and must provide stable command semantics, output formats, and exit codes. The specification is language-neutral; the MVP reference CLI is implemented in Go under `cmd/veriproc`.

## Requirements

### Requirement: CLI Scope And API Only Access

The CLI SHALL interact with VeriProc control-plane state only through the REST API. It MUST NOT write directly to the database, mutate working roots, edit station-derived control artifacts, modify rolling archive publication metadata, submit scheduler jobs directly, or infer backend state by scanning shared storage. Any command that changes backend state MUST map to a documented API operation.

#### Scenario: Query command performs no mutation
- **GIVEN** any query command (e.g. `task list`)
- **WHEN** it is invoked
- **THEN** it issues only HTTP reads and does not submit tasks, request cancellation, alter server configuration, mutate local profile state, or write output files unless explicitly requested by flags

**Additional normative rules for this requirement:**

- **GIVEN** an operator invokes a state-changing CLI command
- **WHEN** the command executes
- **THEN** every backend state change results from a documented REST API operation, and no direct database, filesystem, or scheduler access occurs
### Requirement: CLI Optional For Server Conformance

The CLI SHALL be optional for server conformance: a backend may be conforming without shipping a CLI, though the MVP SHOULD provide one as a reference client. The CLI's own conformance is defined by this capability.

#### Scenario: Backend without CLI
- **GIVEN** a VeriProc backend deployment
- **WHEN** no CLI is shipped with it
- **THEN** the backend may still be conforming with the server-side specification
### Requirement: Identity Distinction Preservation

The CLI MUST preserve all identity distinctions and MUST NOT collapse or reinterpret tasks, runs, jobs, job orders, artifacts, logs, split groups, provenance links, station revisions, processing fingerprints, or scheduler identifiers. When a summary shows multiple related identifiers, their roles MUST be made clear.

#### Scenario: Multi-identifier summary
- **GIVEN** output that includes task, run, and job identifiers together
- **WHEN** the output is rendered
- **THEN** each identifier's role is clearly labeled and the identifiers are not merged or reinterpreted
### Requirement: Opaque Canonical Identifiers

The CLI MUST treat canonical IDs as opaque durable identifiers. It MUST NOT parse time-window, retry, downstream, scheduler, or provenance meaning from task IDs, job IDs, artifact IDs, split-group IDs, or displayed run references. Operator readability MUST come from API-provided fields (station, start, end, state, run references, retry index, classification, timestamps, working-root path).

#### Scenario: Table values from API not derived from IDs
- **GIVEN** `task list` output containing station, start, end, latest run, and canonical run columns
- **WHEN** the table is rendered
- **THEN** those values are taken from the API response and not derived by parsing the task ID

#### Scenario: Run reference is a rendering
- **GIVEN** the preferred compact run reference `<task_id>/r<retry_index>`
- **WHEN** displayed by the CLI
- **THEN** it is treated as a rendering of the canonical composite `(task_id, retry_index)`, not a second identity
### Requirement: Local Convenience Checks

The CLI MAY perform local convenience checks such as unreadable input files, malformed flags, invalid output-format names, missing arguments, or syntactically invalid YAML/JSON. Such checks MUST NOT replace server-side validation; the backend remains authoritative for task validity, station resolution, idempotency, lifecycle transitions, cancellation eligibility, authorization, and consistency.

#### Scenario: Local flag check precedes API call
- **GIVEN** a command with an unsupported `--output` value
- **WHEN** the command is invoked
- **THEN** it fails as a usage error before making an API request

#### Scenario: Local validity is not authority
- **GIVEN** a locally syntactically valid submission document
- **WHEN** the server rejects it as invalid
- **THEN** the CLI surfaces the server validation failure without overriding it with local judgement
### Requirement: Thin Client Behavior

The CLI SHALL remain a thin orchestration client. It may transform flags and input documents into API requests and render responses, but it MUST NOT compute processing fingerprints, decide canonicality, resolve rolling archive inputs, create downstream tasks locally, evaluate split-group readiness locally, or implement scheduler reconciliation locally.

#### Scenario: No local decision engine
- **GIVEN** a task detail response
- **WHEN** rendered
- **THEN** canonicality and classification values come from the API, never computed locally
### Requirement: Stable Command Semantics

Command names, required arguments, flag meanings, output-format names, and exit-code mappings SHOULD remain stable within a CLI major version. Incompatible changes MUST be documented and versioned.

#### Scenario: Documented incompatibility
- **GIVEN** a CLI major-version release that changes a flag meaning
- **WHEN** released
- **THEN** the change is documented in release notes and reflected in the version
### Requirement: No Hidden Mutation

The CLI MUST NOT perform hidden state-changing operations outside the documented API call for the invoked command. Query commands MUST NOT submit tasks, request cancellation, alter server configuration, mutate local profile state, or write output files unless explicitly requested by flags.

#### Scenario: Output file only on request
- **GIVEN** a command that can download content
- **WHEN** no output path flag is supplied
- **THEN** no local file is written as a side effect
### Requirement: Machine Readable Output Modes

The CLI MUST support `--output json`, `--output yaml`, and `--output table` where the command has a structured payload. Machine-readable output MUST be written to standard output with stable field names. JSON output MUST be valid JSON preserving the API response structure or a documented stable projection, and MUST NOT include progress text, prompts, or diagnostics on standard output.

#### Scenario: JSON mode excludes diagnostics
- **GIVEN** a command run with `--output json`
- **WHEN** a warning occurs during execution
- **THEN** the warning goes to standard error and the standard output contains only valid JSON

#### Scenario: YAML mirrors JSON fields
- **GIVEN** the same API response rendered with `--output json` and `--output yaml`
- **WHEN** compared
- **THEN** both preserve the same field set unless a documented projection applies

**Additional normative rules for this requirement:**

YAML output MUST be deterministic and preserve the same fields as JSON unless documented otherwise. Color, styling, and interactive prompts MUST be disabled in machine-readable modes, and output MUST be deterministic for equivalent API responses.
### Requirement: Human Readable Defaults

The default output SHALL be operator-readable: list commands default to compact tabular or aligned summaries; detail commands default to a readable structured view preserving important identifiers and state. Tables MUST NOT imply that readable path names, compact timestamps, station columns, or displayed context fields are alternate identities.

#### Scenario: Table default on terminal
- **GIVEN** `task list` run on a terminal without `--output`
- **WHEN** output is produced
- **THEN** a compact table with stable columns is written to stdout

**Additional normative rules for this requirement:**

Default table output SHOULD be used for list commands when the output stream is a terminal, with stable columns and no truncation of identifiers unless clearly indicated; if truncation is used, machine-readable modes MUST provide full values.
### Requirement: Stdout Stderr Separation

The primary command payload MUST be written to standard output. Diagnostic messages, progress messages, warnings, validation diagnostics, and error summaries MUST be written to standard error unless a command explicitly documents another stream.

#### Scenario: Error to stderr
- **GIVEN** a command that fails with a server error
- **WHEN** the failure is reported
- **THEN** the error summary appears on standard error, not mixed into standard output
### Requirement: Timestamps And Nulls In Output

Timestamps in machine-readable output MUST use the canonical timestamp strings returned by the API; any locally added timestamps MUST be documented and SHOULD use RFC 3339 with explicit timezone. Null values in machine-readable output MUST be represented explicitly when absence is meaningful (e.g. `canonical_run_ref: null`, `canonical_retry_index: null`, `scheduler_identifier: null`).

#### Scenario: Meaningful null preserved
- **GIVEN** a task with no canonical run
- **WHEN** rendered in `--output json`
- **THEN** `canonical_run_ref` and `canonical_retry_index` are explicit JSON nulls

#### Scenario: Human null rendering
- **GIVEN** the same task rendered in table mode
- **WHEN** the canonical run column has no value
- **THEN** it is empty or `none`, without implying a value

**Additional normative rules for this requirement:**

Human-readable output may render nulls as empty cells or `none` but MUST NOT imply a value exists.
### Requirement: Quiet Mode

The CLI MAY support quiet mode for commands where scripts need only an identifier or success indication. Quiet mode MUST be documented per command; for state-changing commands such as `submit`, quiet mode SHOULD still print the primary created or resolved identifier unless the command explicitly supports no-output success.

#### Scenario: Quiet submit prints identifier
- **GIVEN** `submit` with `--quiet`
- **WHEN** submission succeeds
- **THEN** the resolved `task_id` is still printed
### Requirement: Exit Code Model

The CLI MUST use a compact documented exit-code model. Exit code `0` means success, including empty successful query results. Exit code `1` means a generic failure not fitting a more specific category. Exit code `2` means a usage or argument error (syntax, missing arguments, invalid local flags, unreadable input files, unsupported output format, incompatible flag combinations) that prevented a valid API request.

#### Scenario: Empty successful list exits zero
- **GIVEN** `task list` returns an empty result set with HTTP success
- **WHEN** the command exits
- **THEN** the exit code is `0`

#### Scenario: Usage error exits two
- **GIVEN** a command missing a required argument
- **WHEN** invoked
- **THEN** it exits with code `2`

#### Scenario: Server validation failure exits three
- **GIVEN** the server returns a bad-request validation error for a submission
- **WHEN** the CLI reports it
- **THEN** the exit code is `3`

#### Scenario: Missing resource exits four
- **GIVEN** a `task get` for a task id that does not exist or is not visible
- **WHEN** the server returns not found
- **THEN** the CLI reports not found and exits `4`

#### Scenario: Idempotency conflict exits five
- **GIVEN** a submission rejected with an idempotency or invalid-state conflict
- **WHEN** reported
- **THEN** the exit code is `5`

#### Scenario: Unauthorized exits six
- **GIVEN** a request rejected with missing, invalid, or insufficient credentials
- **WHEN** reported
- **THEN** the exit code is `6`

#### Scenario: Unreachable backend exits seven
- **GIVEN** the API endpoint is unreachable or a request times out before a definitive response
- **WHEN** reported
- **THEN** the exit code is `7`

**Additional normative rules for this requirement:**

Exit code `3` means the server rejected the request as invalid (malformed payloads, invalid timestamps, invalid filters, unknown stations reported as invalid request, field-level validation failures). Exit code `4` means not found — the requested task, run, job, artifact, split group, relationship, station revision, or content resource does not exist or is not visible to the caller.
Exit code `5` means conflict or invalid state, including idempotency conflict, invalid state transition, cancellation unsupported as a policy conflict, duplicate request conflict, or reconciliation in progress represented as conflict. Exit code `6` means authentication or authorization failure.
Exit code `7` means unavailable, timeout, or dependency failure of the endpoint, backend dependency, scheduler adapter, database, shared storage, station registry, or rolling archive, or a CLI timeout before a definitive response. Exit code `8` means a partial or indeterminate outcome where the CLI cannot determine whether a state-changing request completed.
### Requirement: Indeterminate Outcome Safety

Exit code `8` SHALL be used only when the CLI cannot determine the outcome of a potentially state-changing request, for example a connection failure after request transmission and before a response. In that case the diagnostic MUST advise the caller to query the relevant resource or retry using the same idempotency key where applicable. The CLI MUST NOT assume failure for an indeterminate submission or cancellation request.

#### Scenario: Post-send disconnect
- **GIVEN** a submission request sent and the connection lost before a response
- **WHEN** the CLI reports the outcome
- **THEN** it exits `8`, does not assume failure, and advises querying or retrying with the same idempotency key
### Requirement: Most Specific Exit Code Wins

If multiple errors occur, the CLI SHALL return the most specific actionable exit code. For example, an authentication failure response from a reachable server MUST return `6`, not `7`. The CLI SHOULD map server error categories from the API error model to exit codes consistently, and MUST preserve a server-provided machine-readable error code in diagnostics and machine-readable error output where possible.

#### Scenario: Auth error not masked as unavailability
- **GIVEN** a reachable server that responds with an authorization failure
- **WHEN** the CLI exits
- **THEN** the exit code is `6`

#### Scenario: Server error code preserved
- **GIVEN** a server error with a machine-readable code
- **WHEN** rendered in JSON error output
- **THEN** the server code, message, request identifier when present, and field-level details are included
### Requirement: Configuration Implementation Status

The rules in this requirement are normative and SHALL be satisfied by the implementation.
**Implementation status.** The implemented CLI supports environment-variable configuration only (`VERIPROC_API_URL`, `VERIPROC_TOKEN`, `VERIPROC_OUTPUT`, `VERIPROC_TIMEOUT`). A configuration file, profiles, and the full precedence chain are not implemented.

#### Scenario: Env-var configuration works
- **GIVEN** `VERIPROC_API_URL` and `VERIPROC_TOKEN` set in the environment
- **WHEN** any command runs
- **THEN** it uses those values for endpoint and authentication
### Requirement: Configuration Precedence Model

The CLI MUST support configuration of the API endpoint and SHOULD support profiles for multiple environments. Configuration MUST follow a documented precedence from lowest to highest: built-in defaults; configuration file defaults; selected profile in the configuration file; environment variables; command-line flags.

#### Scenario: Flag overrides environment
- **GIVEN** a setting present both as an environment variable and as a command-line flag
- **WHEN** the command runs
- **THEN** the command-line flag value takes precedence

#### Scenario: Profile selection before overrides
- **GIVEN** a flag selecting a configuration profile
- **WHEN** configuration is loaded
- **THEN** profile values apply before environment-variable and per-command flag overrides

**Additional normative rules for this requirement:**

At minimum configuration SHOULD support API base URL, API version preference, request timeout, output format default, selected profile name, authentication token reference or credential source, TLS/mTLS settings when required by deployment policy, and proxy settings when appropriate.
### Requirement: Configuration Sources And Local File Rules

The CLI SHOULD define a default user-level configuration path (on POSIX, e.g. `~/.config/veriproc/config.yaml`) and MAY support a project-local configuration file; project-local configuration MUST NOT silently override explicit environment variables or flags. Environment variables SHOULD use a stable `VERIPROC_` prefix covering at least API base URL, selected profile, authentication token or token file, request timeout, and default output format.

#### Scenario: Secrets not printed
- **GIVEN** a configuration-inspection command
- **WHEN** run with default flags
- **THEN** token values are not printed

#### Scenario: Project-local config does not override env
- **GIVEN** a project-local config file setting the API URL and an explicit `VERIPROC_API_URL` in the environment
- **WHEN** a command runs
- **THEN** the environment variable wins

**Additional normative rules for this requirement:**

Commands that inspect effective configuration MUST avoid printing secret values by default, and any command writing local CLI configuration affects only the caller's CLI configuration, not server configuration.
### Requirement: Secrets Handling In Errors

The CLI MUST avoid printing secrets in ordinary diagnostics, debug output, or machine-readable command payloads. If credentials are missing or rejected, the command MUST use the authentication or authorization exit code (`6`).

#### Scenario: Rejected token
- **GIVEN** an invalid or missing token
- **WHEN** the server rejects the request
- **THEN** the CLI exits `6` and never echoes the token value
### Requirement: Submit Command

`submit` SHALL create a task submission request mapping to `POST /tasks`. In the target model it MUST accept a structured request document from a file or standard input (YAML input SHOULD be supported; JSON MAY be supported mapping to the same canonical semantics) and MAY accept flags for common fields such as destination `station_id`, window start, window end, idempotency key, force flag, priority, parent task, parent run, and client metadata.

#### Scenario: Successful flag submission
- **GIVEN** `submit --station --start --end` with valid values and a reachable backend
- **WHEN** the API accepts the submission
- **THEN** output includes the `task_id`, state, destination, window, force flag, and any run-reference fields returned by the API

#### Scenario: Idempotent replay visible
- **GIVEN** a submission with an idempotency key that matched an existing record
- **WHEN** the API returns the existing task
- **THEN** the CLI indicates the match and shows the existing `task_id`

#### Scenario: No local record on rejection
- **GIVEN** a server-side rejection of a submission
- **WHEN** the CLI reports the error
- **THEN** no local task record is created

**Additional normative rules for this requirement:**

On success, output MUST include the submitted or existing `task_id`, task state, destination, processing window, force flag, and any latest-run or canonical-run reference fields returned by the API (`latest_retry_index`, `latest_run_ref`, `canonical_retry_index`, `canonical_run_ref`); if the API indicates an idempotency-record match, the CLI SHOULD make that visible.
The command MUST surface server validation failures, idempotency conflicts, unknown destination errors, dependency failures, and authorization failures, and MUST NOT create a local task record when the server rejects a submission.


**Implementation status.** The implemented `submit` accepts flag-based input only (`--station`, `--start`, `--end`, plus force/idempotency options); `--file`/stdin document input is not implemented.
### Requirement: Submit Idempotency Key Handling

The CLI MAY allow an idempotency key in a request document, a command-line flag, or both. If supplied in multiple places, the values MUST match or the command MUST fail with a usage error before submission. The CLI MAY generate an idempotency key only when explicitly requested by the operator or a documented automation mode, and MUST NOT silently generate a key in a way that changes duplicate-submission behavior without the caller's knowledge.

#### Scenario: Conflicting idempotency keys
- **GIVEN** a document and a flag each supplying different idempotency keys
- **WHEN** submit runs
- **THEN** it fails with a usage error (`2`) before contacting the API

#### Scenario: No silent key generation
- **GIVEN** a submit without any idempotency key
- **WHEN** the request is built
- **THEN** the CLI does not add a generated key unless an explicit option requests one
### Requirement: Submit Force Semantics

The force flag MUST map to the API force field. The CLI MUST NOT reinterpret force as permission to overwrite canonical runs, delete prior outputs, replace deduplication records, bypass output validation, or create downstream tasks from non-canonical runs. Forced-run canonicality is determined by backend policy.

#### Scenario: Force is pass-through
- **GIVEN** `submit --force`
- **WHEN** the request is sent
- **THEN** the API force field is set true and no additional local semantics are applied
### Requirement: Submit Parent And Destination Handling

Parent references supplied via flags or documents MUST be passed to the API as parent task and parent run references; a parent run reference MUST include the parent task and retry index, either as separate fields or a documented compact form. The CLI MAY check parent identifiers for syntactic non-emptiness but MUST NOT decide parent consistency locally beyond obvious malformed input.

#### Scenario: Malformed parent rejected locally
- **GIVEN** an empty parent task identifier
- **WHEN** submit is invoked
- **THEN** it fails with a usage error

#### Scenario: Unknown station is server-side
- **GIVEN** a syntactically valid but nonexistent destination station
- **WHEN** submit runs
- **THEN** the server's unknown-destination error is surfaced with its exit code, not decided locally

**Additional normative rules for this requirement:**

Destination MUST be specified by `station_id`, and the backend remains authoritative for resolving unknown destinations.
### Requirement: Submit Timestamp Acceptance

The rules in this requirement are normative and SHALL be satisfied by the implementation.

#### Scenario: Compact timestamp accepted
- **GIVEN** `--start 20260513T131429`
- **WHEN** submit builds its request
- **THEN** the value is accepted and normalized to the API's canonical form

#### Scenario: Local window order check
- **GIVEN** a start timestamp later than the end timestamp
- **WHEN** submit is invoked
- **THEN** the CLI may reject it as a usage error before submission

**Additional normative rules for this requirement:**

Processing-window timestamps supplied as flags SHOULD be accepted in RFC 3339 form (whole-second and millisecond precision, with or without timezone; without timezone interpreted as UTC), in compact UTC forms `YYYYMMDDTHHmmSS` and `YYYYMMDDTHHmmSSmmm`, and in date-only forms `YYYY-MM-DD` and `YYYYMMDD` (midnight UTC). The CLI MAY reject a local start greater than an end before submission.
The server remains authoritative for accepted precision, timezone normalization, and deployment-specific timestamp rules.
### Requirement: Submit Error Surfacing

Submission errors returned by the server MUST be surfaced without hiding the server's machine-readable error code. Human-readable output SHOULD show the error message and field-level validation details; machine-readable output MUST include the server error code, message, request identifier when present, and any field-level details returned by the API.

#### Scenario: Field-level details in JSON errors
- **GIVEN** a server validation failure with field-level details
- **WHEN** rendered with `--output json`
- **THEN** the error code, message, request id, and field details are all present
### Requirement: No Local Semantics Invention

The CLI MUST NOT invent task semantics not present in the API: no local deduplication based on station and time window, no local processing-fingerprint computation, no local downstream task creation, no local split-group membership assignment, and no local rolling archive input resolution.

#### Scenario: No local deduplication
- **GIVEN** two submit invocations with the same station and window but no idempotency key
- **WHEN** both are sent
- **THEN** both reach the API without local deduplication short-circuiting either request
### Requirement: Document Flag Merge Behavior

When both a request document and command-line flags are supplied, the CLI MUST document the merge behavior: either reject conflicting duplicate fields or use a simple documented override rule. If flags override document fields, the behavior MUST be explicit in help text and MUST NOT silently change safety-relevant fields such as `force`, parent references, or destination.

#### Scenario: Conflicting force value
- **GIVEN** a document with `force: false` and a `--force` flag
- **WHEN** submit runs
- **THEN** the documented merge rule applies and the outcome is visible in the resulting request
### Requirement: Task Get Command

`task get` SHALL retrieve one task by identifier, mapping to `GET /tasks/{task_id}`. Output MUST identify the task, destination, processing window, force flag, task state, parent references when present, split-group reference when present, latest run, canonical run, timestamps, and relationship links or summaries when returned by the API. If the task does not exist or is not visible to the caller, the command MUST report a not-found error and use exit code `4`.

#### Scenario: Task detail fields
- **GIVEN** a task with canonical and latest runs
- **WHEN** `task get` renders it
- **THEN** destination, window, force flag, state, parent and split-group references when present, latest and canonical run references, and timestamps are shown

#### Scenario: Missing task
- **GIVEN** a task identifier the caller cannot see
- **WHEN** `task get` runs
- **THEN** a not-found error is reported and the exit code is `4`
### Requirement: Task List Command

`task list` SHALL list or search tasks through API-backed query endpoints mapping to `GET /tasks` with query parameters. It SHOULD accept filters corresponding to the API: destination `station_id`, task state, processing-window overlap or containment, creation time range, parent task, parent run, split group, force flag, and canonical-run presence.

#### Scenario: Station and state filter
- **GIVEN** `task list --station scen2 --state running`
- **WHEN** executed
- **THEN** only tasks matching both filters are listed from the API response

#### Scenario: Default table columns
- **GIVEN** `task list` in table mode
- **WHEN** rendered
- **THEN** columns include task id, station, start, end, state, latest run ref, canonical run ref, and created-at, all sourced from API fields

#### Scenario: Empty result is success
- **GIVEN** filters matching no tasks
- **WHEN** the API returns an empty successful response
- **THEN** the command exits `0`

**Additional normative rules for this requirement:**

Default output SHOULD include task identifier, station, start, end, state, latest run reference, canonical run reference, and creation time; machine-readable output MUST preserve the list envelope, pagination metadata, applied filters, and ordering when returned by the API. Invalid filters or pagination parameters MUST be reported as usage errors if detected locally before the API call, or as server validation errors if returned by the API.


**Implementation status.** The implemented `task list` supports `--station`, `--state`, and `--split-group`; window, creation-time-range, parent-run, and canonical-presence filters are not implemented (tracking API filter gaps).
### Requirement: Run Get Command

`run get` SHALL retrieve one run by task-scoped run reference, accepting either `--task <task_id> --retry <retry_index>` or the compact form `<task_id>/r<retry_index>`. It MUST map to an API operation equivalent to `GET /runs/{run_id}` (run addressing by internal `run_id` in the current implementation, with the API resolving the compact form).

#### Scenario: Compact run reference
- **GIVEN** `run get <task_id>/r5`
- **WHEN** executed
- **THEN** the run detail for retry index 5 of that task is rendered

#### Scenario: Opaque working root
- **GIVEN** a run with an exposed working-root path
- **WHEN** rendered
- **THEN** the path is displayed exactly as returned by the API without local interpretation

#### Scenario: Hidden fields not reconstructed
- **GIVEN** the API omits a sensitive field
- **WHEN** detail output is rendered
- **THEN** the CLI does not attempt local filesystem access to reconstruct it

**Additional normative rules for this requirement:**

Output MUST identify the run reference, task, station revision, run state, canonical classification, retry index, working-root path when exposed by the API (displayed as an opaque API-provided value), processing fingerprint or reference, job-order artifact when present, manifest reference, active job, executor type and execution node when exposed, timestamps, failure reason when present, and finalization summary when present.
A missing or inaccessible run MUST be reported as not found, and if the API hides sensitive fields the CLI MUST NOT attempt to reconstruct them through local filesystem access.
### Requirement: Run List Command

`run list` SHALL list or search runs through API-backed endpoints, mapping to `GET /tasks/{task_id}/runs` for task-scoped listing or `GET /runs` for broader filtered search. It SHOULD accept filters for task identifier, station identifier, run state, and canonical classification.

#### Scenario: Task-scoped run listing
- **GIVEN** `run list --task <task_id>`
- **WHEN** executed
- **THEN** all runs of the task are listed with retry index and run reference

#### Scenario: Station from API not from path
- **GIVEN** a run whose working-root path embeds a station-like string
- **WHEN** the station column is rendered
- **THEN** the value comes from API fields, not parsed from the working-root path

**Additional normative rules for this requirement:**

Default output SHOULD include task identifier, retry index, run reference, station, start, end, state, canonical classification, executor type when known, failure reason when present, creation time, and working-root path when relevant and exposed. The command MUST obtain station and time-window values from API run summaries, embedded task summaries, or documented projections, and MUST NOT derive them from the run reference or working-root path.
An empty successful result MUST exit with success; a failed query MUST be distinguished from it.


**Implementation status.** The implemented `run list` supports `--task`, `--station`, and `--state`. Window, timestamp-range, parent-run, fingerprint, canonical, and scheduler-id filters are not implemented (tracking API filter gaps).
### Requirement: Job Get Command

`job get` SHALL retrieve one scheduler job record by VeriProc job identifier, or where supported by scheduler identifier via `--scheduler-id`, mapping to `GET /jobs/{job_id}` or a documented job query endpoint.

#### Scenario: SLURM detail fields
- **GIVEN** an implemented job-detail view of a SLURM job
- **WHEN** rendered
- **THEN** scheduler identifier, native state, last-observed timestamp, and available SLURM specifics are shown

#### Scenario: No direct scheduler contact
- **GIVEN** possibly stale scheduler-derived state
- **WHEN** displayed
- **THEN** the last-observed timestamp from the API is shown and no direct scheduler query occurs

**Additional normative rules for this requirement:**

Output MUST include job identifier, task-scoped run reference, executor type, scheduler identifier when known, scheduler-native state, submission timestamp, last-observed timestamp when available, cancellation fields when present, and reconciliation status when relevant; for SLURM jobs, detailed output SHOULD include canonical mode (`slurm-native` or `slurm-docker`), partition, account, QoS, resources, node list, elapsed time, scheduler exit code, and wrapper log references when the API provides them.
If scheduler-derived state is stale, the CLI MUST display the server-provided last-observed timestamp and MUST NOT query the scheduler directly unless the API explicitly exposes a server-side refresh operation.


**Implementation status.** A standalone `job get` command is not implemented. Job inspection is available through `run jobs` and the API-level `GET /jobs/{job_id}`. This section is retained as the target command set.
### Requirement: Job Listing

Job listing (`job list` or `run jobs`) SHALL list jobs associated with a run or search jobs through API-backed filters, mapping to `GET /tasks/{task_id}/runs/{retry_index}/jobs` or `GET /jobs`. It SHOULD accept a task-scoped run reference and MAY accept executor type, scheduler state, submission time range, or reconciliation-status filters if the API supports them.

#### Scenario: Jobs for a run
- **GIVEN** a run with jobs
- **WHEN** `run jobs <task_id>/rN` executes
- **THEN** the jobs are listed with identifiers, states, and timestamps

#### Scenario: No jobs is empty success
- **GIVEN** a run with no jobs
- **WHEN** job listing executes
- **THEN** an empty successful result is reported and the exit code is `0`

**Additional normative rules for this requirement:**

Default output SHOULD include job identifier, task identifier, retry index, run reference, executor type, scheduler identifier, scheduler-native state, submission timestamp, and last-observed timestamp, with SLURM mode, partition, and node list included when they fit stable columns. If the run does not exist, the command MUST report not found; if the run exists but has no jobs, it SHOULD report an empty successful result.
### Requirement: Artifact Listing

`artifact list` SHALL list artifact metadata for a task-scoped run reference or task identifier, accepting filters such as logical artifact type, file type, validation status, canonical-run only, latest-run only, and publication status. It MUST map to API operations equivalent to `GET /tasks/{task_id}/runs/{retry_index}/artifacts` or `GET /tasks/{task_id}/artifacts`.

#### Scenario: Artifact metadata columns
- **GIVEN** a run with published artifacts
- **WHEN** `artifact list` executes
- **THEN** artifact id, type, kind, validation status, availability, and publication summary from the API are shown without local file reads

**Additional normative rules for this requirement:**

Metadata output MUST identify artifact identifier, producing run or external origin, logical artifact type, object kind, path or access reference subject to access policy, availability status, creation or discovery timestamp, validation status, checksum when available, and publication summary when available. Artifact listing MUST show metadata rather than reading local files.
### Requirement: Artifact Get Not Implemented

The rules in this requirement are normative and SHALL be satisfied by the implementation.
**Implementation status.** `artifact get` (artifact content download to `--output-file`) is not implemented; artifact content is accessible only through the REST API.

#### Scenario: No silent overwrite on download
- **GIVEN** the implemented download behavior in the target model and a local file already at the output path
- **WHEN** a download is attempted
- **THEN** it fails rather than overwriting, unless an explicit overwrite flag is supplied

**Additional normative rules for this requirement:**

In the target model, `artifact get` accepts an artifact identifier and maps to `GET /artifacts/{artifact_id}` or a documented artifact-content endpoint. If artifact bytes are unavailable due to retention or access policy but metadata exists, the CLI MUST distinguish unavailable content from missing metadata when the API makes that distinction. When downloading to a caller-specified path, the CLI SHOULD fail rather than overwrite an existing local file unless an explicit overwrite flag is supplied; local download affects only the caller's filesystem and MUST NOT be represented as a VeriProc artifact publication or backend state change.
### Requirement: Content Denial Distinction

If the API denies access to content but allows metadata access, the CLI MUST show the metadata allowed by policy and report content denial separately. If the API reports content unavailable due to retention, storage failure, or pending validation, the CLI MUST report that state without attempting direct filesystem access.

#### Scenario: Metadata shown despite content denial
- **GIVEN** an artifact whose content is access-denied but metadata is readable
- **WHEN** inspected
- **THEN** the metadata is displayed and the content denial is reported as a separate condition
### Requirement: Logs Command

`logs` SHALL inspect log metadata and retrieve log content for a run through the API, mapping to `GET /tasks/{task_id}/runs/{retry_index}/logs`, `GET /tasks/{task_id}/runs/{retry_index}/logs/{log_name}`, or `GET /artifacts/{artifact_id}/content`. It MUST accept a task-scoped run reference and MAY accept a log name, artifact identifier, byte range, line range, cursor, `--follow`, or output destination if the API supports those modes.

#### Scenario: Log content unpolluted
- **GIVEN** log content printed to standard output
- **WHEN** no decorated mode is selected
- **THEN** no headers, prompts, colors, or table formatting are added, so redirection is safe

#### Scenario: No direct working-root reads
- **GIVEN** a `logs` invocation
- **WHEN** retrieving content
- **THEN** all content comes from API endpoints, never from working-root paths

**Additional normative rules for this requirement:**

Metadata output MUST identify log artifact identity and producing run; content output SHOULD stream or print log bytes without adding formatting that would corrupt the content unless a decorated mode is explicitly selected. The CLI MUST NOT read log files directly from a working root; if the API reports content unavailable, the CLI MUST report it and use the mapped exit code.
### Requirement: Relationships Command

The rules in this requirement are normative and SHALL be satisfied by the implementation.
**Implementation status.** The `relationships` command is not implemented; there is no backing API endpoint and no CLI command. This section is retained as the target model.

#### Scenario: Provenance from API only
- **GIVEN** a provenance query in the target model
- **WHEN** relationships are returned
- **THEN** all link fields come from the API response with no local reconstruction from text or filenames

**Additional normative rules for this requirement:**

In the target model, `relationships` queries machine-readable provenance links for tasks, runs, artifacts, split groups, and aggregation workflows. It SHOULD accept source/target object types and identifiers, relationship type, split-group identifier, or convenience flags `--task`, `--run`, `--artifact`, with run references supplied in task-scoped form. It MUST map to `GET /tasks/{task_id}/relationships`, `GET /tasks/{task_id}/runs/{retry_index}/relationships`, or a generic relationship query endpoint. Output MUST preserve source object type, source identifier, target object type, target identifier, relationship type, contribution role when present, creation reason when present, and creation timestamp when returned. The command MUST NOT reconstruct provenance by parsing routing-history text, inferring from filenames, or scanning rolling archives.
### Requirement: Cancel Command

`cancel` SHALL request cancellation of a run through the API, mapping to `POST /tasks/{task_id}/runs/{retry_index}/cancel`, and MUST NOT contact the scheduler directly. It MUST accept a task-scoped run reference and MAY accept `--reason`, `--yes`, `--quiet`, or output-format flags.

#### Scenario: Request vs completed
- **GIVEN** a cancellation accepted while the run is not yet terminal
- **WHEN** output is rendered
- **THEN** the accepted-request state is clearly distinguished from a completed `cancelled` state

#### Scenario: Already-terminal target
- **GIVEN** a cancellation request for a run already terminal
- **WHEN** the server responds
- **THEN** the CLI surfaces the server response, creates no additional side effect, and handles repeated requests idempotently where the API supports it

**Additional normative rules for this requirement:**

Output MUST distinguish cancellation request accepted from cancellation completed, showing the current run state and, when available, scheduler cancellation request state, active job, and timestamps. The command MUST surface invalid state transitions, unsupported cancellation, reconciliation in progress, authorization failures, dependency failures, and not-found responses, preserving the server error code and mapping it to the appropriate exit code.
### Requirement: Cancel Confirmation Policy

Because cancellation is state-changing, an interactive CLI SHALL ask for confirmation by default when standard input is a terminal; automation MUST be able to bypass the prompt with a documented flag such as `--yes`. If standard input is not a terminal and no explicit confirmation flag is supplied, the CLI SHOULD either require `--yes` or follow a documented non-interactive safety policy.

#### Scenario: Interactive prompt by default
- **GIVEN** `cancel` with standard input a terminal and no `--yes`
- **WHEN** invoked
- **THEN** a confirmation prompt appears identifying the run reference

#### Scenario: Non-interactive without flag
- **GIVEN** `cancel` with standard input not a terminal and no `--yes`
- **WHEN** invoked
- **THEN** the CLI requires `--yes` or applies its documented non-interactive safety policy

#### Scenario: No local state marking
- **GIVEN** any cancellation outcome
- **WHEN** output is produced
- **THEN** run state comes exclusively from API responses; the CLI never marks a run cancelled itself

**Additional normative rules for this requirement:**

The prompt SHOULD identify the run reference, current state when available, and a concise warning that cancellation is a request that may not stop execution immediately, and MUST NOT imply that cancellation deletes tasks, runs, jobs, logs, working roots, artifacts, or provenance. The CLI MUST NOT locally mark a run as cancelled and SHOULD warn when finalization, publication, downstream routing, or scheduler state may still be in progress per the server response.
### Requirement: Cancel Outcome Distinctions

Successful cancellation output SHALL distinguish, when the API exposes them: request accepted with the run not yet terminal; cancellation completed with the run `cancelled`; the run already terminal before or during the request; and cancellation undetermined because reconciliation is required.

#### Scenario: Reconciliation-required outcome
- **GIVEN** the API reports cancellation cannot be determined pending reconciliation
- **WHEN** the CLI reports it
- **THEN** the outcome is presented as undetermined/reconciliation-required, not as success or failure
### Requirement: Health Command

`health` SHALL report backend liveness as exposed by the API, requiring no resource identifier and mapping to a backend health endpoint. Output SHOULD include overall health status, backend version or build metadata when exposed, and request timing when useful. If the backend cannot be reached or returns an unhealthy response, the command MUST return an unavailable or generic failure exit code per the exit-code model.

#### Scenario: Unhealthy backend
- **GIVEN** a backend returning an unhealthy response
- **WHEN** `health` executes
- **THEN** the unhealthy state is reported and the exit code maps to unavailability or generic failure

**Additional normative rules for this requirement:**

Liveness MUST NOT be treated as proof that submission, scheduler interaction, shared storage, or station registry access is available unless the backend health endpoint explicitly defines those semantics.
### Requirement: Readiness Command

`readiness` SHALL report whether the backend is ready for control-plane operations, requiring no resource identifier and mapping to a backend readiness endpoint. Output SHOULD include overall readiness and dependency states returned by the API (database, scheduler adapter, station registry, shared storage, rolling archive configuration, migration state when exposed), including names, states, last-checked times, and diagnostics subject to access policy.

#### Scenario: Dependency failure surfaced
- **GIVEN** a readiness response reporting the database unavailable
- **WHEN** `readiness` renders it
- **THEN** the dependency name and state from the server response are shown

#### Scenario: No local probing
- **GIVEN** a `readiness` invocation
- **WHEN** the API endpoint is reachable
- **THEN** readiness is determined solely by the backend readiness endpoint, not by CLI-side dependency probes

**Additional normative rules for this requirement:**

The CLI MUST NOT perform its own dependency probing in place of the backend readiness endpoint, and local network reachability MUST NOT be treated as equivalent to readiness. If the readiness endpoint reports a dependency unavailable, the CLI MUST surface that server-provided state.
### Requirement: Version Command

`version` SHALL report the CLI version, SHOULD report build metadata, supported command schema version when defined, and supported API versions, and SHOULD work without contacting the backend. A flag such as `--check-api` MAY query the configured backend's API version metadata and report compatibility status. Failure to contact the backend during an explicit API compatibility check MUST be reported without hiding the local CLI version.

#### Scenario: Offline version
- **GIVEN** no reachable backend
- **WHEN** `version` runs without `--check-api`
- **THEN** the CLI version is printed successfully

#### Scenario: Check-api failure keeps local version
- **GIVEN** `version --check-api` with an unreachable backend
- **WHEN** executed
- **THEN** the contact failure is reported and the local CLI version is still printed

**Additional normative rules for this requirement:**

If API compatibility cannot be determined, the CLI SHOULD fail clearly for commands that depend on unsupported or unknown API behavior, while commands not requiring server contact remain usable.


**Implementation status.** `version --check-api` exists; its API-compatibility checking and failure behavior (§6.12) are not yet covered by tests.
### Requirement: Unsupported API Version Failure

The rules in this requirement are normative and SHALL be satisfied by the implementation.
If a backend serves an unsupported API version, the CLI SHOULD fail clearly before performing state-changing operations. A CLI version SHOULD declare which API versions it supports, and a command compatible with a subset of the backend API MAY be allowed while warning that other commands are unsupported.

#### Scenario: Refuse state change on unsupported API
- **GIVEN** a backend serving an API version the CLI does not support
- **WHEN** a state-changing command is attempted
- **THEN** the CLI fails clearly before issuing the state-changing request
### Requirement: Config Validate Command

The rules in this requirement are normative and SHALL be satisfied by the implementation.
**Implementation status.** `config validate` is not implemented; station and instance configuration validation happens at backend startup. This section is retained as the target model.

#### Scenario: Local-only validation semantics
- **GIVEN** the target `config validate` running locally against a valid document
- **WHEN** it succeeds
- **THEN** the output makes clear it is local schema validation only and does not guarantee server acceptance

**Additional normative rules for this requirement:**

In the target model, `config validate` MAY validate local YAML or JSON documents as an operator convenience. If validation is local, it MUST be described as local schema validation only; if server-driven, it MUST call a documented validation API endpoint. In both cases, validation MUST NOT mutate server state. Output SHOULD identify document type, schema version, validation outcome, and field-level errors when available. Local validation success MUST NOT imply the server will accept a later state-changing request; the server remains authoritative for station existence, instance configuration identity, access policy, deployment-specific validation, and submission semantics.
### Requirement: Task Delete Command

`task delete` SHALL permanently remove a task, its descendant tasks, all of their runs, every dependent control-plane record, and the on-disk working roots of those runs. It MUST accept a task identifier, MUST accept `--dry-run` to preview affected records without deleting, and MUST accept `--force` to skip the interactive confirmation prompt. It MUST map to `DELETE /api/v1/tasks/{task_id}`, sending `?dry_run=true` for previews.

#### Scenario: Dry-run preview
- **GIVEN** `task delete <task_id> --dry-run`
- **WHEN** executed
- **THEN** a preview of affected tasks, runs, per-table counts, and working roots to be removed is displayed and nothing is deleted

#### Scenario: Confirmation before destruction
- **GIVEN** `task delete <task_id>` without `--force` on an interactive terminal
- **WHEN** invoked
- **THEN** a dry-run preview is shown and a confirmation prompt precedes the destructive request

#### Scenario: Deletion summary
- **GIVEN** a successful destructive `task delete`
- **WHEN** output is rendered
- **THEN** per-table row counts, task and run counts, and removed working-root count are summarized, with any filesystem removal errors surfaced

**Additional normative rules for this requirement:**

Without `--force`, the CLI MUST first request a dry-run preview, display the affected records, and prompt for confirmation before issuing the destructive request. Output MUST summarize per-table deleted-row counts, the number of selected tasks and runs, and the number of working roots removed (or to be removed in a dry run), and filesystem removal errors MUST be surfaced.
The command MUST surface not-found, authorization, and dependency failures, and MUST report an indeterminate outcome (exit `8`) when the result of a destructive request cannot be determined.
### Requirement: Run Delete Command

`run delete` SHALL permanently remove a single run, its dependent control-plane records, and its on-disk working root, leaving the owning task and sibling runs intact. It MUST accept a task-scoped run reference (`TASK_ID/rN`) or an internal run identifier, and MUST accept `--dry-run` and `--force` with the same semantics as `task delete`.

#### Scenario: Single-run deletion scope
- **GIVEN** a task with multiple runs
- **WHEN** `run delete` removes one run
- **THEN** the owning task and sibling runs remain intact

#### Scenario: Compact reference resolution
- **GIVEN** `run delete <task_id>/r3 --dry-run`
- **WHEN** executed
- **THEN** the CLI resolves the compact reference to the internal run id and requests a dry-run preview

**Additional normative rules for this requirement:**

It MUST resolve the run reference to an internal run identifier when necessary and map to `DELETE /api/v1/runs/{run_id}`, sending `?dry_run=true` for previews. Without `--force`, the CLI MUST preview and prompt before deleting. Output MUST summarize per-table deleted-row counts and whether the working root was removed. The command MUST surface not-found, authorization, and dependency failures, and report an indeterminate outcome when a destructive request's result is unknown.
### Requirement: Clean Command Basis Semantics

`clean` SHALL permanently remove all tasks (with descendants, runs, dependent records, and working roots) whose timestamps fall within a requested time range. It MUST accept `--before TIMESTAMP` and/or `--after TIMESTAMP` and MUST accept `--by BASIS` (default `processing-time`) and `--dry-run`/`--force`.

#### Scenario: Processing-time before cutoff
- **GIVEN** `clean --before 2025-05-29T10:00:00Z --by processing-time --dry-run`
- **WHEN** executed
- **THEN** tasks with `created_at` at or before the cutoff are selected

#### Scenario: Processing-window basis
- **GIVEN** `clean --after 2025-05-29T10:00:00Z --by processing-window`
- **WHEN** executed
- **THEN** tasks whose window starts at or after the cutoff are selected

#### Scenario: Both cutoffs select a range
- **GIVEN** both `--after` and `--before` supplied
- **WHEN** selection occurs
- **THEN** tasks within the inclusive `[after, before]` range on the chosen basis are selected

#### Scenario: Invalid basis rejected
- **GIVEN** `--by nonsense`
- **WHEN** `clean` is invoked
- **THEN** validation fails with a usage error

**Additional normative rules for this requirement:**

The `--by` basis selects which task timestamps the cutoffs compare against: `processing-time` compares against task `created_at` (`--before T` selects tasks created at or before `T`; `--after T` selects tasks created at or after `T`), and `processing-window` compares against the data sensing window (`--before T` selects tasks whose window ends at or before `T`; `--after T` selects tasks whose window starts at or after `T`).
Supplying both cutoffs selects tasks within `[after, before]`. An unrecognized `--by` value MUST fail validation.
### Requirement: Clean Timestamp Formats

`clean` timestamps MUST accept RFC 3339 (`2025-05-29T10:00:00Z`), RFC 3339 without timezone (`2025-05-29T10:00:00`, interpreted as UTC), compact UTC (`20250529T100000`), ISO 8601 date (`2025-05-29`, midnight UTC), and compact date (`20250529`, midnight UTC) forms.

#### Scenario: Compact date form
- **GIVEN** `clean --before 20250529`
- **WHEN** the cutoff is parsed
- **THEN** it is interpreted as midnight UTC on 2025-05-29

#### Scenario: Timezone-less timestamp
- **GIVEN** `clean --before 2025-05-29T10:00:00`
- **WHEN** parsed
- **THEN** it is interpreted as UTC
### Requirement: Clean Dry Run Force And Output

`clean` MUST map to `POST /api/v1/maintenance/clean` with `before`, `after`, `basis`, and `dry_run` fields. Without `--force`, the CLI MUST first request a dry-run preview, display the affected tasks, runs, and artifacts, and prompt for confirmation before the destructive request.

#### Scenario: Preview before destructive clean
- **GIVEN** `clean --before T` without `--force`
- **WHEN** invoked
- **THEN** a dry-run preview is displayed and confirmation precedes the destructive request

#### Scenario: Missing cutoff from server
- **GIVEN** a server invalid-request response citing a missing cutoff
- **WHEN** reported
- **THEN** the CLI surfaces the invalid-request error with the mapped exit code

**Additional normative rules for this requirement:**

Output MUST summarize per-table deleted-row counts, the number of selected tasks and runs, and the number of working roots removed (or to be removed in a dry run), with filesystem removal errors surfaced. The command MUST surface invalid-request responses (including a missing cutoff and invalid timestamps), authorization failures, and dependency failures, and report an indeterminate outcome when a destructive request's result is unknown.
### Requirement: Clean Help Without Cutoffs

When invoked with neither `--before` nor `--after`, `clean` MUST print help describing the available options and exit successfully without contacting the API.

#### Scenario: Bare clean prints help
- **GIVEN** `clean` invoked with no cutoff flags
- **WHEN** it executes
- **THEN** help text describing options is printed, the exit code is `0`, and no API request is made
### Requirement: Clean Station Restriction

The rules in this requirement are normative and SHALL be satisfied by the implementation.
The implemented CLI provides `clean --station` as an additive extension restricting the cleanup scope to a single station; it is not in conflict with the time-bounded cleanup model above.

#### Scenario: Station-scoped clean
- **GIVEN** `clean --station scen2 --before T --dry-run`
- **WHEN** executed
- **THEN** only tasks for the given station within the cutoff range are previewed for deletion
### Requirement: Listing Filters Backend Alignment

List commands SHALL provide filter flags corresponding to documented API filters. If a filter is not supported by the configured backend API version, the CLI MUST either omit it from that version's help output or fail with a clear unsupported-filter error before or after the API response.

#### Scenario: Unsupported filter
- **GIVEN** a filter not supported by the configured backend version
- **WHEN** used without local-emulation documentation
- **THEN** the CLI reports a clear unsupported-filter error or omits it from help

**Additional normative rules for this requirement:**

The CLI SHOULD NOT emulate unsupported server-side filters by fetching broad result sets and filtering locally unless the command explicitly documents that behavior and the result set is bounded enough to avoid misleading pagination or ordering.
### Requirement: Pagination Mapping

Pagination flags SHALL map to the API pagination model. If the API uses continuation tokens, the CLI SHOULD expose those tokens without interpreting their internal structure; if offsets are used, offset and limit SHOULD be exposed consistently with the API. The CLI MAY provide a convenience option to follow all pages for automation, but MUST document that multiple API requests may be made and MUST preserve the deterministic ordering returned by the server.

#### Scenario: Continuation token passthrough
- **GIVEN** an API response with a continuation token
- **WHEN** list output is rendered in machine-readable mode
- **THEN** the token is exposed without interpretation

**Additional normative rules for this requirement:**

The CLI MUST NOT imply that local display sorting changes server-side pagination semantics.
### Requirement: Interactive Table Paging

In table output mode, list commands MAY page interactively: when standard input and standard output are both terminals, the CLI MAY prompt for the next page and MUST print the continuation token when the operator stops, so the listing can be resumed with the documented pagination flag. When standard output is not a terminal, the CLI MUST NOT prompt; it MAY follow all pages automatically, in which case it MUST document the multiple API requests and preserve server ordering.

#### Scenario: Piped output never prompts
- **GIVEN** a list command piped to another process
- **WHEN** output is produced
- **THEN** no paging prompt appears

#### Scenario: Token printed on stop
- **GIVEN** interactive paging with the operator stopping after the first page
- **WHEN** the listing ends
- **THEN** the continuation token is printed so the query can be resumed

**Additional normative rules for this requirement:**

Interactive paging MUST be disabled in machine-readable output modes.
### Requirement: Empty Versus Failed Distinction

List commands MUST distinguish an empty successful result from an error. An empty successful result MUST exit with success unless a command explicitly defines a non-empty expectation flag.

#### Scenario: Empty success
- **GIVEN** a filter matching nothing with a successful API response
- **WHEN** the command exits
- **THEN** the result is shown as empty and exit code is `0`
### Requirement: Latest And Canonical Distinct Fields

Task output MUST expose latest-run and canonical-run references distinctly when present, using stable fields such as `latest_retry_index`, `latest_run_ref`, `canonical_retry_index`, and `canonical_run_ref`. Run output MUST expose canonical classification distinctly from run state. The CLI MUST NOT imply that the latest run is canonical unless the API explicitly identifies it as canonical.

#### Scenario: Non-canonical latest run
- **GIVEN** a task whose latest run is not canonical
- **WHEN** task output is rendered
- **THEN** the latest-run and canonical-run fields are shown separately without implying the latest run is canonical
### Requirement: Unknown Field Tolerance

The CLI MUST tolerate unknown optional fields in API responses: it SHOULD preserve unknown fields in machine-readable passthrough modes when practical, and MUST NOT fail solely because the server adds optional fields, links, enum values, or metadata the CLI does not understand, unless those fields are required for a command's safety semantics. The CLI MAY reject unknown values in local flags when the value cannot be mapped to a documented API parameter.

#### Scenario: New optional field tolerated
- **GIVEN** an API response containing an unknown optional metadata field
- **WHEN** rendered in `--output json`
- **THEN** the field is preserved or at least does not cause a failure
### Requirement: Summary And Detail Views

The rules in this requirement are normative and SHALL be satisfied by the implementation.
Commands SHOULD support summary and detail views: summaries SHOULD include stable identifying and state fields suitable for scanning; detail views SHOULD preserve all API fields relevant to the resource subject to format constraints, and machine-readable output SHOULD include the complete API payload or a documented stable projection.

#### Scenario: Detail preserves failure reason
- **GIVEN** a failed run viewed in detail
- **WHEN** rendered
- **THEN** the failure reason and relevant identifiers from the API are preserved
### Requirement: Default Ordering

Default ordering SHOULD follow the API default ordering. If ordering flags are exposed, they MUST map to documented API ordering parameters.

#### Scenario: Server ordering preserved
- **GIVEN** a paginated API response in a documented order
- **WHEN** rendered
- **THEN** the server ordering is preserved across pages
### Requirement: Streaming And Decorated Log Output

When printing regular-file artifact or log content to standard output, the CLI SHOULD avoid adding headers, prompts, colors, or table formatting unless a decorated view is explicitly selected, so scripts can redirect content safely. Diagnostics and download progress MUST be written to standard error. Directory artifact content access MUST follow the API's documented mode; the CLI MUST NOT assume a directory artifact can be streamed as file bytes.

#### Scenario: Script-safe content output
- **GIVEN** artifact content printed to stdout
- **WHEN** redirected to a file
- **THEN** the bytes are unmodified and diagnostics went to stderr
### Requirement: Consistency Not Oversold

The CLI SHOULD NOT promise stronger consistency than the API provides. If the API documents eventual consistency for scheduler-derived or filesystem-derived fields, the CLI MUST expose the relevant observed timestamps or availability states rather than hiding lag.

#### Scenario: Staleness visible
- **GIVEN** a scheduler-derived field that is eventually consistent
- **WHEN** displayed
- **THEN** the associated last-observed timestamp or availability state is shown
### Requirement: Additive Implemented Commands

The rules in this requirement are normative and SHALL be satisfied by the implementation.
**Implementation status.** Beyond the commands documented above, the implemented CLI provides `promote`, `group list`, `group get`, `group close`, `station topology`, `station summary`, `clean --station`, and `--cascade`/`--quiet` flags on delete/clean. These commands are additive, not in conflict with this chapter, and are covered here for inventory only.

#### Scenario: Additive command inventory
- **GIVEN** the implemented CLI binary
- **WHEN** help is enumerated
- **THEN** the documented commands plus the additive extension commands are present without conflicting semantics
### Requirement: Non Goals And No Second Control Path

The CLI MUST NOT be treated as a substitute for direct API integration in all cases: batch systems, dashboards, and other clients may integrate with the REST API directly.

#### Scenario: No server mutation from local utility
- **GIVEN** a local convenience utility such as the target `config validate`
- **WHEN** it runs
- **THEN** it performs no server-side mutation of stations, instance configuration, database state, rolling archives, or working roots

**Additional normative rules for this requirement:**

The CLI MUST NOT provide an undocumented second control path; local utilities may validate documents for convenience but MUST NOT publish station revisions, modify instance configuration, mutate database state, create rolling archive entries, write working-root control files, or bypass API validation unless explicit administrative API semantics are defined.
Rich terminal UIs, graphical workflow editing, live dashboards, shell completion, interactive form-based submission, and long-running operator consoles are out of scope for the MVP unless provided as optional extensions preserving the same API-first and identity-preserving rules.
