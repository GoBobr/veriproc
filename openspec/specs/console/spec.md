# Console Specification

## Purpose

This specification defines the VeriProc operator web console and multi-instance dashboard,
converted from the former normative operator-web-console specification chapter
(source document removed; see git history),
with as-built details reflected in `docs/docs/10-web-console.md`. The console is an optional
lightweight web application for monitoring and controlling one or more `veriprocd` instances
through a browser, modeled closely on the S4P operator view: one row per station, compact
execution-slot boxes, and shell-free operator control. Unless informative, all statements
here are normative for implementations claiming operator web console support.

## Requirements

### Requirement: Goals and Scope

The console SHALL provide multi-instance monitoring and operator control capabilities while
acting strictly as a client of the VeriProc control plane.

#### Scenario: Console bypasses nothing

- **GIVEN** the console gateway is deployed
- **WHEN** the console reads or controls VeriProc state
- **THEN** it MUST NOT bypass the REST API by writing directly into the SQLite state store, the station
  configuration root, the working-root filesystem, or scheduler state
- **AND** for task browsing and preview the gateway MAY read the shared filesystem directly only when
  that access is explicitly configured
- **AND** direct writes MUST remain forbidden

**Additional normative rules for this requirement:**

- monitor several `veriprocd` instances on one page
- render one station row per station in each instance block
- render a fixed-width strip of execution boxes per station, with empty slots visible
- show per-station success and failure counters for an operator-selected time interval
- allow authorized operators to pause and unpause a station, submit a task using explicit start and
  end times, cancel a running or queued run, and retry an eligible failed or cancelled task
- open a dedicated task page for a selected non-empty execution box
- browse the selected run working root and quick-view ASCII files and logs without leaving the task page

### Requirement: Three-Layer Architecture

The deployment model SHALL consist of a browser frontend, a console gateway, and one or more
upstream `veriprocd` instances, and the browser MUST communicate only with the console gateway.
- the console gateway SHOULD be implemented in Go
- the frontend SHOULD be a lightweight single-page application (Preact/TypeScript with Vite recommended)
- WebSockets are optional; server-sent events SHOULD be preferred over WebSockets if push semantics are needed

#### Scenario: Responsibilities split

- **GIVEN** the three-layer architecture
- **WHEN** responsibilities are divided
- **THEN** the frontend is responsible for layout, interaction, routing, and fast refresh of already-shaped
  view models
- **AND** the console gateway is responsible for authenticating end users, enforcing role-based
  authorization, storing operator-facing configuration and preferences, polling upstream instance state,
  aggregating station and run metadata into UI-oriented responses, reading working-root trees, logs, and
  previewable files from configured shared-filesystem roots, translating operator actions into authorized
  upstream API calls, and auditing mutating actions
- **AND** each upstream `veriprocd` remains authoritative for its own task, run, job, artifact, and station
  state, and the shared filesystem remains authoritative for working-root file content

#### Scenario: No direct browser access to upstream

- **GIVEN** a browser using the console
- **WHEN** it renders the dashboard
- **THEN** direct browser access to upstream `veriprocd` instances is not required

### Requirement: Multi-Instance Dashboard Grid

The main page SHALL present a grid of instance blocks that collapses responsively on narrow screens
without losing core controls.
- a typical wide-screen layout SHOULD allow three blocks per row
- on narrower screens the grid MUST collapse to two columns and then one column

#### Scenario: Instance block contents

- **GIVEN** the dashboard is rendered
- **WHEN** an instance block is displayed
- **THEN** it MUST contain an instance title and health indicator, a last-refresh timestamp, a date-time
  picker selecting the beginning of the statistics interval, optional quick presets (e.g. last 6h/24h/3d/7d),
  and a scrollable list of station rows
- **AND** the selected interval MUST mean from the selected timestamp until current backend time
- **AND** counters in that instance block MUST be computed against that interval

### Requirement: Station Row Rendering

Each station row SHALL render a fixed station label on the left, a strip of a configurable number of
visible execution slots (default 12), and per-interval success and failure counters on the right.

#### Scenario: Slot semantics and colors

- **GIVEN** a station row is rendered
- **WHEN** each slot is displayed
- **THEN** an empty slot is rendered in neutral gray
- **AND** a running run is rendered in an active accent color such as blue
- **AND** a queued, accepted, or dispatching run is rendered in a neutral pending color such as slate or gray
- **AND** failed runs and failed tasks with no run ever created MUST remain visible in red until an operator
  explicitly hides them
- **AND** completed runs MUST remain visible for a configurable visibility timeout (30s recommended) and MUST
  disappear automatically after the backend observes successful terminal completion and the timeout expires

#### Scenario: Deterministic slot ordering

- **GIVEN** visible slot occupants
- **WHEN** they are ordered
- **THEN** the ordering MUST be deterministic: running runs oldest first by run start time, completed runs
  within the visibility timeout newest first by terminal timestamp, queued or accepted runs oldest first by
  task creation time, failed or cancelled runs and immediately-failed tasks still visible newest first by
  terminal timestamp, then empty slots
- **AND** if an occupant lacks terminal or creation timestamps the gateway MAY use another stable deterministic
  fallback such as upstream list order or retry index

#### Scenario: Overflow badge

- **GIVEN** more non-empty runs exist than visible slots
- **WHEN** the row is rendered
- **THEN** the row MUST display an overflow badge such as `+5` rather than silently truncating the count

#### Scenario: Interval counters

- **GIVEN** the right side of a station row
- **WHEN** counters are displayed
- **THEN** `cnt_succ` MUST count runs or tasks reaching successful completion during the selected interval and
  `cnt_fail` MUST count runs or tasks reaching failed or cancelled terminal state during the interval
- **AND** the implementation MUST document whether counters are task-based or run-based, with run-based
  counting preferred

### Requirement: Hide Failure Acknowledgment

Hiding a failed run SHALL be an operator acknowledgment action affecting dashboard visibility only.

#### Scenario: Hiding preserves state

- **GIVEN** an operator hides a failed run from the dashboard
- **WHEN** the hide is applied
- **THEN** it MUST remove the run from the station row
- **AND** it MUST NOT delete run records, logs, artifacts, or working-root content from disk

### Requirement: Station Context Menu

Right-clicking a station name SHALL open a context menu; for viewers it MUST NOT expose mutating actions.

#### Scenario: Operator station actions

- **GIVEN** an operator user right-clicks a station name
- **WHEN** the menu opens
- **THEN** it MUST offer pause station, unpause station, and submit task
- **AND** only one of pause or unpause MAY be enabled at a time according to current station state

#### Scenario: Submit dialog validation

- **GIVEN** an operator opens the submit flow
- **WHEN** the dialog is displayed
- **THEN** it MUST contain a read-only station identifier, start time, end time, optional force checkbox,
  optional client metadata or comment field, and submit and cancel actions
- **AND** it SHOULD support RFC 3339 entry and a browser-native date-time control
- **WHEN** start and end times are invalid or inverted
- **THEN** the submission MUST be rejected by request validation

### Requirement: Task-Box Context Menu

Right-clicking a task box SHALL open a context menu scoped to the represented run or task.

#### Scenario: Box actions per state

- **GIVEN** an operator user right-clicks a task box
- **WHEN** the run is running or queued
- **THEN** the menu MUST offer kill or cancel
- **WHEN** the run is failed or cancelled and terminal
- **THEN** the menu MUST offer retry, which creates a new run for the underlying task, and if still visible
  it MUST offer hide-from-view acknowledgment
- **WHEN** the run is a completed canonical run
- **THEN** retry SHOULD NOT be offered by default
- **AND** the UI label MAY be `Kill` but the backend API operation MUST remain `cancel`
- **AND** hide-from-view MUST affect dashboard visibility only and MUST NOT delete records, files, logs, or artifacts

#### Scenario: Box click navigation

- **GIVEN** a non-empty task box
- **WHEN** the user primary-clicks it
- **THEN** the task detail page MUST open in a new browser tab

### Requirement: Task Detail Page Layout

The task detail page SHALL contain three main panes: general task and run information, a working-root
browser, and a quick-view pane.

#### Scenario: Header information

- **GIVEN** the task detail page
- **WHEN** the header is rendered
- **THEN** it MUST display `task_id`, current selected run reference, station identifier, task window start
  and end, run state, upstream instance identifier, working-root path when access policy allows, and
  links or buttons for cancel, retry, and refresh when authorized

#### Scenario: Run selector

- **GIVEN** a task with more than one run
- **WHEN** the run selector is used to switch runs
- **THEN** the working-root browser and quick-view data MUST refresh for the selected run without leaving the page

### Requirement: Working-Root Tree Browser

The working-root browser SHALL allow browsing only inside the selected run working root.

#### Scenario: Browser contents

- **GIVEN** the task page is open
- **WHEN** the browser is displayed
- **THEN** it MUST show directories, regular files, file size, modification time when known, and a file kind
  hint such as text, log, binary, directory, or unknown
- **AND** it SHOULD start at the working-root top level and make common folders such as `input`, `output`,
  and `logs` easy to reach

### Requirement: Quick-View Pane

The quick-view pane SHALL stay on the task page while the operator browses files, showing inline content
for ASCII or UTF-8 text files including logs, and metadata plus a download action for non-text files.
- the pane SHOULD support lazy loading for large files, a line-wrapping toggle, a fixed-width font, and
  byte- or line-range fetch for logs larger than the inline limit
- the implementation MUST define a maximum inline preview size, with 1 MiB per request recommended

#### Scenario: Preview failure reason

- **GIVEN** a file preview fails because the file is too large, binary, or missing
- **WHEN** the quick-view pane renders
- **THEN** it MUST display a precise reason instead of a blank pane

### Requirement: Station Activity Table

Clicking a station label SHALL open a station activity page in a new browser tab displaying a paginated,
sortable table of all runs for that station.

#### Scenario: Table columns

- **GIVEN** the station activity page
- **WHEN** runs are listed
- **THEN** the table MUST contain at minimum `task_id` linked to the task detail page, `retry_index` rendered
  as a coloured badge, `created_at`, `working_root`, and a coloured `state` button that opens the task detail
  page in a new tab when clicked
- **AND** adjacent runs belonging to the same task SHOULD be visually grouped

#### Scenario: Sorting and pagination

- **GIVEN** the runs table
- **WHEN** the operator interacts with it
- **THEN** it MUST support server-side sorting by `run_id` (default), `created_at`, and `task_id` with
  ascending and descending direction, and clicking a column header MUST toggle the sort direction
- **AND** it MUST support cursor-based pagination with a configurable page size, default 50 configurable via
  the `ui.station_runs_page_size` key, and a page-size selector offering 10, 25, 50, and 100 runs per page
- **AND** status colours MUST be consistent with the dashboard slot colour scheme

#### Scenario: Station runs endpoint

- **GIVEN** the console gateway
- **WHEN** the frontend requests station runs
- **THEN** the gateway MUST expose a station runs endpoint that proxies run-list queries to the upstream
  `GET /api/v1/runs` endpoint scoped to the station identified by the path parameter
- **AND** it MUST pass through pagination, sorting, and state filter query parameters and return the upstream
  response including the `next_cursor` pagination token

### Requirement: Gateway Configuration

The console gateway SHALL load a dedicated configuration file (`veriproc.console/v1` schema) defining
HTTP bind address and TLS settings, console-local authentication sources, the list of managed instances
with per-instance upstream base URL and credentials reference, per-instance shared-filesystem working-root
base, dashboard defaults, console SQLite database path and migration policy, and optional frontend bundle
directory, session settings, and audit-retention settings.

#### Scenario: Secret handling

- **GIVEN** gateway configuration
- **WHEN** upstream bearer tokens are configured
- **THEN** the gateway SHOULD NOT store them in plaintext in SQLite
- **AND** environment variables, file-backed secrets, or an external secret manager are preferred

#### Scenario: Read-only filesystem access

- **GIVEN** configured working-root bases
- **WHEN** the gateway accesses them
- **THEN** it SHOULD have read-only access and SHOULD NOT require write access to those roots

**Additional normative rules for this requirement:**

- dashboard defaults MUST include visible slot count, completed-run visibility timeout, refresh interval,
  station activity page size, and the console page title (browser tab title and top-bar heading,
  default `VeriProc Operator Console`, configurable via the `ui.page_title` key)

### Requirement: Console SQLite Data Model

The console database SHALL be distinct from the upstream control-plane database and store only
console-local state, with minimum tables for users (or equivalent subject registry), api_tokens (storing
the effective console role with the authenticated subject; session tables optional), instance_preferences,
hidden_station_runs (or equivalent acknowledgment table), and audit_log.

#### Scenario: Cookie-session CSRF note

- **GIVEN** the console authentication model
- **WHEN** cookie-based sessions are used
- **THEN** mutating endpoints MUST use CSRF protection
- **AND** the reference implementation uses token-based (not cookie-session) authentication, in which case
  the CSRF requirement does not apply

#### Scenario: Console Local Token Table Not Authoritative

- **GIVEN** a gateway configured with an access profile
- **WHEN** the gateway resolves the identity of a console user
- **THEN** the identity is resolved from the access profile and not from the console-local token table

**Additional normative rules for this requirement:**

- when an access profile is configured, the api_tokens table SHALL NOT be the authority for
  authenticating console users, and SHALL NOT be populated from user-supplied token definitions
- optional tables MAY store saved filters, column preferences, or per-user dashboard layout
- the hidden-run table MUST be console-local, identifying target instance, station, task, and retry index,
  recording acting user and timestamp, and allowing the renderer to suppress an acknowledged failed run
  without mutating upstream state

### Requirement: Users and Roles

The console SHALL support exactly two effective user classes: `viewer` (read dashboards, task pages, and
logs) and `operator` (all viewer actions plus submit, pause, unpause, cancel, retry, and hide of failed runs).

#### Scenario: Upstream role mapping

- **GIVEN** the gateway reuses upstream VeriProc bearer tokens
- **WHEN** roles are mapped
- **THEN** backend role `reader` MUST map to console role `viewer` and backend role `operator` to console
  role `operator`
- **AND** the gateway MUST ensure a console `operator` never escalates beyond the configured upstream
  operator permissions

#### Scenario: Password storage

- **GIVEN** local passwords are used
- **WHEN** they are stored
- **THEN** they MUST be salted and hashed, never plaintext
- **AND** token-based or SSO-backed authentication is allowed

### Requirement: Audit Log

Every mutating action initiated from the console SHALL write an audit record containing at minimum timestamp,
acting user, target instance, action type, target station, task, or run as applicable, request payload
summary, and outcome status.

#### Scenario: Structured process log

- **GIVEN** a mutating console action
- **WHEN** it completes
- **THEN** implementations SHOULD also emit a structured process log entry so operator activity is visible
  in the console service log at normal operational log levels

#### Scenario: Upstream mutation failure audited

- **GIVEN** a mutating action fails upstream
- **WHEN** the failure is handled
- **THEN** the console MUST show the upstream error reason when safe to expose
- **AND** it MUST record the failure in the audit log

### Requirement: Gateway Minimum API Surface

The console gateway SHALL expose UI-oriented dashboard endpoints rather than forcing the browser to assemble
many upstream calls. The minimum endpoints are:

#### Scenario: Pre-shaped dashboard response

- **GIVEN** the dashboard endpoint
- **WHEN** it responds
- **THEN** the response MUST already contain station rows expanded into a UI-friendly shape with instance
  status, `since`, `last_refresh`, per-station paused state, success/failure counts, slot occupants with
  kinds and task/retry identity, and an overflow count

**Additional normative rules for this requirement:**

- `GET /api/console/instances`
- `GET /api/console/instances/{instance_id}/dashboard?since=...`
- `POST /api/console/instances/{instance_id}/stations/{station_id}/pause`
- `POST /api/console/instances/{instance_id}/stations/{station_id}/unpause`
- `POST /api/console/instances/{instance_id}/stations/{station_id}/submissions`
- `GET /api/console/instances/{instance_id}/stations/{station_id}/config`
- `POST /api/console/instances/{instance_id}/tasks/{task_id}/runs/{retry_index}/cancel`
- `POST /api/console/instances/{instance_id}/tasks/{task_id}/runs/{retry_index}/hide?station_id=...`
- `POST /api/console/instances/{instance_id}/tasks/{task_id}/retry`
- `GET /api/console/instances/{instance_id}/tasks/{task_id}`
- `GET /api/console/instances/{instance_id}/tasks/{task_id}/runs`
- `GET /api/console/instances/{instance_id}/tasks/{task_id}/runs/{retry_index}/tree`
- `GET /api/console/instances/{instance_id}/tasks/{task_id}/runs/{retry_index}/file?path=...`
- `GET /api/console/instances/{instance_id}/stations/{station_id}/runs?limit=...&cursor=...&sort=...&order=...`

### Requirement: Stable Console Identity

The console gateway SHALL treat `task_id` and `retry_index` as the stable console identity.

#### Scenario: Run-id resolution

- **GIVEN** the reference upstream API exposes some run actions by internal `run_id`
- **WHEN** the gateway needs to call a run-id-scoped upstream operation
- **THEN** it MAY resolve the corresponding `run_id` through `GET /api/v1/tasks/{task_id}/runs` or
  `GET /api/v1/runs?task_id=...` first

### Requirement: Upstream Capability Slice

The upstream capabilities required by the console SHALL exist somewhere authoritative — directly in
`veriprocd` or in a tightly coupled adapter layer — and MUST be treated as a concrete implementation
slice verified with automated unit tests before or alongside the console gateway rollout.

#### Scenario: Reference upstream API

- **GIVEN** a console gateway targeting this repository
- **WHEN** consuming upstream capabilities
- **THEN** it SHOULD use the reference `/api/v1` endpoints: `GET /api/v1/stations`,
  `GET /api/v1/stations/summary?since=...`, `GET /api/v1/stations/{station_id}/summary?since=...`,
  `POST /api/v1/stations/{station_id}/pause`, `POST /api/v1/stations/{station_id}/unpause`, and
  `GET /api/v1/tasks/{task_id}/runs`
- **AND** it SHOULD reuse existing upstream endpoints for tasks, runs, retry, cancel, jobs, artifacts, and logs

**Additional normative rules for this requirement:**

Minimum upstream capabilities:

- list stations with current paused or active state
- summarize queued and running work per station
- summarize failed or cancelled runs per station with stable task and retry identity
- list failed tasks by station and terminal state to surface immediately-failed tasks with no run
- pause a station
- unpause a station
- retry an eligible task
- retrieve run lists for a task

### Requirement: Station Summary Contract

For each station, the upstream or adapter layer SHALL expose station identifier, paused state, count of
currently running runs, count of currently queued or accepted tasks, an ordered list of visible slot
occupants before console-local hidden-run suppression, success and failure counters for a
caller-supplied interval, and a last-authoritative-refresh timestamp.

#### Scenario: Gateway merges hidden runs

- **GIVEN** an upstream station summary
- **WHEN** the gateway builds the dashboard response
- **THEN** it MUST merge the summary with its own hidden-run acknowledgment table before returning it
- **AND** completed runs MUST remain visible only until the configured completed-run visibility timeout expires
- **AND** failed or cancelled runs MUST remain visible until hidden through the console acknowledgment action
- **AND** console-local hiding MUST never call an upstream deletion operation and never mutate upstream run,
  task, artifact, log, or working-root state

#### Scenario: Presentation responsibilities

- **GIVEN** the upstream station summary
- **WHEN** it is returned
- **THEN** it contains only non-empty visible occupants
- **AND** empty slots, overflow badges, and hidden-run suppression are presentation responsibilities of the
  console gateway and frontend
- **AND** if the gateway needs an overflow count beyond what the summary includes, it MUST compute it from
  summary counts plus additional task/run queries or document that overflow is unavailable

#### Scenario: Enriching failed tasks with no run

- **GIVEN** the upstream summary is run-derived and omits failed tasks that never created a run
- **WHEN** the gateway prepares the dashboard
- **THEN** it MAY supplement the failed-slot population from `GET /api/v1/tasks` or an equivalent
  authoritative task-list endpoint, keyed by `(task_id, retry_index)` or another stable console identity

#### Scenario: Visibility timeout alignment

- **GIVEN** the reference upstream summary uses a visible slot limit of 12 and a 30s completed-run visibility
  timeout
- **WHEN** the gateway configures its own timeout
- **THEN** it MAY apply a shorter presentation timeout locally
- **AND** if it exposes a longer timeout it MUST supplement the summary from task/run queries or reject that
  configuration, because the upstream summary may already have omitted aged-out completed runs

#### Scenario: Pause semantics

- **GIVEN** a paused station
- **WHEN** new tasks are accepted
- **THEN** pausing prevents dispatch of prepared `ready` runs for that station while accepted tasks may still
  be admitted and prepared into queued ready runs, so operators can see queued work accumulate

#### Scenario: Upstream authorization

- **GIVEN** upstream authentication is enabled
- **WHEN** station endpoints are called
- **THEN** mutating endpoints require upstream `operator` authorization and read endpoints require a valid
  reader or operator token

**Additional normative rules for this requirement:**

- `GET /api/v1/stations` MUST return an `items` array with at minimum `station_id`, `station_name` when
  known, and `paused`
- `GET /api/v1/stations/summary?since=...` MUST return `since` and `items`, using a default interval of
  approximately 24 hours when `since` is omitted, with each item containing `station_id`, `station_name`
  when known, `paused`, `running_count`, `queued_count`, `counts.success`, `counts.failure`, `slots`, and
  `last_refresh`
- `GET /api/v1/stations/{station_id}/summary?since=...` MUST return the same summary object under the
  `station` field together with `since`
- summary slot occupants MUST contain `kind` (one of `running`, `completed`, `queued`, `failed`),
  `run_id` when applicable, `task_id`, `retry_index`, `state`, and `terminal_at` for terminal runs when known

### Requirement: Upstream Capability Testing

The upstream `veriprocd` capabilities required by the console SHALL be covered by automated unit tests and, where practical, narrow handler-level tests. The upstream test suite MUST verify at minimum the capabilities listed under the additional rules below, and MUST NOT require a live scheduler or live browser; it SHOULD use table-driven service tests, fake stores, and narrow HTTP handler tests.

#### Scenario: Station capability coverage
- **GIVEN** the upstream unit-test suite
- **WHEN** station listing, summary aggregation, retry, pause/unpause, and run-list responses are exercised
- **THEN** paused-state reporting, aggregation counters, authorization enforcement, deterministic ordering, and stable task and retry identity are all verified without a live scheduler

**Additional normative rules for this requirement:**

The upstream test suite MUST verify at minimum:

- station listing and paused-state reporting
- station-summary aggregation for running, queued, completed-with-timeout, failed, and cancelled runs
- retry eligibility and retry endpoint behavior
- pause and unpause endpoint behavior and authorization enforcement
- run-list retrieval for a task with deterministic ordering
- stable task and retry identity in every response needed by the console

- the upstream test suite MUST NOT require a live scheduler or live browser
- it SHOULD use table-driven service tests, fake stores, and narrow HTTP handler tests

### Requirement: Gateway Filesystem Access Contract

Working-root browsing and preview SHALL be implemented by the console gateway through direct filesystem
access, not through upstream `veriprocd` API endpoints, and MUST obey containment rules.

#### Scenario: Containment enforcement

- **GIVEN** a file-browser or preview request
- **WHEN** the gateway resolves the target path
- **THEN** the root MUST be the selected run working root only
- **AND** path traversal outside the working root MUST be rejected
- **AND** symbolic links resolving outside the working root MUST be rejected or clearly marked and blocked
  according to policy
- **AND** the gateway SHOULD read from configured working-root bases only, using read-only access

#### Scenario: Preview safety

- **GIVEN** a preview response for a file exceeding the inline limit
- **WHEN** the response is returned
- **THEN** it MUST declare truncation
- **AND** binary files MUST NOT be silently decoded as text

### Requirement: Authorization Enforcement

The console gateway SHALL enforce authorization server-side, even though the frontend hides controls the
current user cannot use.
- `viewer` MAY call dashboard, task, run, tree, file-preview, and station-config endpoints
- `viewer` MUST NOT call pause, unpause, submit, cancel, retry, or hide endpoints
- `operator` MAY call all read endpoints and all mutating endpoints

#### Scenario: Server-side enforcement

- **GIVEN** a viewer-authenticated request to a mutating endpoint
- **WHEN** the gateway processes it
- **THEN** the request MUST be rejected regardless of what the frontend displays

#### Scenario: Viewer Reads Station Config

- **GIVEN** a viewer-authenticated request to the station config endpoint
- **WHEN** the gateway processes it
- **THEN** the request succeeds and the gateway proxies it to the upstream config endpoint

### Requirement: Refresh and Performance

The rules in this requirement are normative and SHALL be satisfied by the implementation.
The dashboard SHOULD feel near-real-time without creating excessive upstream load.
- dashboard polling SHOULD occur every 5 to 15 seconds
- task-detail refresh SHOULD occur every 10 to 30 seconds for state metadata
- logs and file previews SHOULD be loaded on demand
- per-instance gateway caching SHOULD use a short TTL, typically 2 to 5 seconds

#### Scenario: Degraded instance isolation

- **GIVEN** one upstream instance is slow or unavailable
- **WHEN** the dashboard refreshes
- **THEN** a failing instance block MUST NOT block rendering of healthy instances
- **AND** the failing block MUST render as degraded with the last successful refresh time when known

### Requirement: Visual Design Essentials

The UI SHALL be visually sparse and operational rather than decorative, with the required visual properties and recommended palette listed under the additional rules below.

#### Scenario: State distinguishability
- **GIVEN** station rows rendering slots in different lifecycle states
- **WHEN** the dashboard view is displayed
- **THEN** running, waiting, paused, degraded, and unavailable states are clearly distinguishable using high-contrast state colors with a legend available

**Additional normative rules for this requirement:**

Required visual properties:

- dense station rows optimized for scan speed
- high-contrast state colors with a legend available
- consistent box size across the page
- clear distinction between running, waiting, paused, degraded, and unavailable states
- keyboard-accessible menus and dialogs

Recommended palette: empty slot neutral gray, running slot green, waiting slot blue, completed slot light
green, failed slot red, cancelled slot amber, paused station label or badge orange or amber.

### Requirement: Console Gateway Unit Tests

The console gateway unit tests SHALL cover, at minimum, configuration loading and validation
(instances, UI defaults, secret references); authentication/authorization middleware for `viewer` and
`operator`; dashboard aggregation merging upstream station summaries with console-local hidden-run state;
slot rendering model generation including overflow calculation; hide-acknowledgment repository behavior

#### Scenario: Coverage status (known gaps)

- **GIVEN** the current implementation
- **WHEN** coverage is assessed
- **THEN** gateway tests cover configuration, auth/authz for `viewer`/`operator`, dashboard aggregation,
  hidden-run idempotency and suppression, hide-run audit-log emission, filesystem-safety and preview
  behavior, and upstream action dispatch
- **AND** audit-log tests cover only the hide action, not every mutating endpoint (pause/unpause/submit/cancel/retry);
  upstream-client timeout handling and cache invalidation are untested
- **AND** these gaps are tracked as Known Issues

**Additional normative rules for this requirement:**

- gateway tests SHOULD use table-driven coverage with fake upstream clients rather than live `veriprocd`
including idempotent hide requests; audit-log emission for every mutating action; upstream client adapters
including request shaping, timeout handling, and structured error mapping; direct filesystem browsing and
preview handlers with working-root base enforcement; path normalization and containment enforcement; and
response serialization for dashboard, task-detail, tree, and preview endpoints.

- the gateway test suite MAY assume the upstream capabilities of §8.9 exist and are independently
  unit-tested in `veriprocd`

### Requirement: Frontend Unit Tests

The frontend unit tests SHALL cover, at minimum, instance-grid rendering for one, several, and unavailable
instances; station-row rendering for each slot kind and overflow case; role-gated menus so viewers never
see mutating actions and operators do; right-click context-menu behavior on station labels and task boxes;
date-time filter state changes and dashboard refresh requests; task-box click navigation to the task-detail

#### Scenario: Coverage status (known gaps)

- **GIVEN** the current implementation
- **WHEN** frontend coverage is assessed
- **THEN** coverage is thin relative to §8.14.2: instance-grid rendering of degraded instances, task-box
  click navigation, task-page run switching, working-root tree browsing behavior, date-time filter state
  changes, and upstream action failure display are not covered
- **AND** these gaps are tracked as Known Issues

**Additional normative rules for this requirement:**

- frontend tests SHOULD use component-level tests with mocked API responses
page in a new tab or equivalent routed behavior; run selector behavior on the task page; working-root tree
browsing and quick-view pane updates; and user-visible handling of upstream action failures and degraded
refresh states.

- snapshot-only coverage is NOT sufficient without behavioral assertions

### Requirement: Unit Testing Edge Cases

The unit-testing suite SHALL include explicit coverage for these edge cases: a failed run hidden and the
dashboard reloaded; a completed run aging out of visibility while the dashboard remains open; multiple
failed runs for the same task with different `retry_index` values; simultaneous running, queued, completed,
and failed runs in one station row; a hide request repeated for an already hidden run; an operator retrying

#### Scenario: Coverage status (known gaps)

- **GIVEN** the current implementation
- **WHEN** §8.14.3 edge cases are assessed
- **THEN** hidden-run reload and repeated-hide are covered
- **AND** multiple failed runs per task, retrying a still-visible failed task, completion aging-out
  mid-session, and preview exactly at the size limit are not covered

**Additional normative rules for this requirement:**

a failed task whose failed run is still visible in red; one managed instance unavailable while others
continue refreshing normally; preview of a log file exactly at the preview-size limit and just beyond it.

### Requirement: Additional Required Test Coverage

The overall console unit-testing suite SHALL also verify, at minimum, the behaviors listed under the additional rules below.

#### Scenario: Submission dialog validation
- **GIVEN** the submission dialog with start and end time fields
- **WHEN** invalid or inverted start and end times are submitted
- **THEN** the console rejects the request locally before dispatching it upstream

**Additional normative rules for this requirement:**

The suite SHALL also verify, at minimum: submission-dialog request validation
including invalid or inverted start and end times; cancel, retry, pause, unpause, and hide action dispatch
behavior including upstream success and upstream failure cases; file-tree browsing safety including rejection
of path traversal and blocked symlink escape; text-preview behavior for ASCII, UTF-8, binary, truncated, and
oversized files; task-page run switching when a task has multiple runs; cache invalidation and refresh
behavior when upstream state changes between polls; and automatic disappearance of completed runs after the
configured visibility timeout.

### Requirement: Test Isolation

Unit tests SHALL NOT require a live browser, a live scheduler, or a live upstream `veriprocd` process, and
MUST run in isolation using fakes, mocks, or temporary filesystem fixtures.

#### Scenario: Deterministic filesystem fixtures

- **GIVEN** working-root browsing or preview logic is tested
- **WHEN** tests exercise it
- **THEN** they MUST use temporary directories and explicit fixtures so containment and file-kind behavior
  are deterministic

### Requirement: Station Config View

The station activity page SHALL provide a `Config` control in its header, positioned next to the
Pause and Unpause controls, that opens a modal dialog displaying the station's fully resolved
configuration as a read-only, scrollable field.

#### Scenario: Config Button Opens Dialog

- **GIVEN** the station activity page
- **WHEN** the user activates the `Config` control
- **THEN** a modal dialog opens showing the resolved configuration document fetched from the gateway
  config endpoint
- **AND** the dialog is read-only and scrollable, and its content includes the container image
  reference with its version tag

#### Scenario: Freshness Indication

- **GIVEN** the config dialog is open
- **WHEN** the response metadata is rendered
- **THEN** the dialog shows the configuration `content_hash` and `effective_at` so the operator can
  tell whether the view reflects the current daemon startup

#### Scenario: Config Fetch Failure

- **GIVEN** the upstream instance is unavailable or returns an error for the config request
- **WHEN** the dialog is opened
- **THEN** the dialog displays the error reason instead of configuration content

### Requirement: Console Authentication Source

The console gateway SHALL authenticate console users against an access profile shared with the
managed instances, so that a subject recognized by the daemons is recognized by the console with the
same subject name and the same effective role, and SHALL stop accepting console-local token
definitions as an independent credential source once a profile is configured.

#### Scenario: Shared Identity Consistent

- **GIVEN** an access profile shared by a daemon and the console gateway
- **WHEN** the same token authenticates against both
- **THEN** both resolve the same subject and the same effective console role

#### Scenario: Profile Replaces Local Token Definitions

- **GIVEN** a gateway configured with an access profile
- **WHEN** a token that appears only in the gateway's own legacy inline token list is presented
- **THEN** the request is rejected as unauthenticated

### Requirement: Console Identity Introspection

The console gateway SHALL expose an endpoint that reports the effective identity of the calling
principal, returning the subject and the effective console role, and SHALL report an anonymous
subject when the request carries no credential and anonymous browsing is enabled.

#### Scenario: Authenticated Identity Reported

- **GIVEN** a request presenting a valid profile token
- **WHEN** the identity endpoint is called
- **THEN** the response reports the profile subject and the effective console role for the token

#### Scenario: Anonymous Identity Reported

- **GIVEN** a gateway with anonymous browsing enabled
- **WHEN** the identity endpoint is called without a credential
- **THEN** the response reports the reserved anonymous subject and the read-only effective role

#### Scenario: Rejected Credential Reports Rejection

- **GIVEN** a gateway with anonymous browsing enabled
- **WHEN** the identity endpoint is called with a credential that matches no profile entry
- **THEN** the request is rejected as unauthenticated with a message stating that no profile entry
  matches the credential
- **AND** the request is NOT downgraded to an anonymous session

### Requirement: Console Anonymous Browsing

The console gateway SHALL support an anonymous-browsing mode in which requests without a credential
are admitted as a read-only principal on read endpoints, and SHALL never admit an anonymous principal
on a mutating endpoint.

#### Scenario: Anonymous Read Admitted

- **GIVEN** the gateway running in anonymous-browsing mode
- **WHEN** a request without a credential calls a read endpoint
- **THEN** the request is processed and the response contains no secret material

#### Scenario: Anonymous Mutation Rejected

- **GIVEN** the gateway running in anonymous-browsing mode
- **WHEN** a request without a credential calls a mutating endpoint
- **THEN** the request is rejected as unauthenticated with a message stating that a token is required

#### Scenario: Anonymous Browsing Disabled

- **GIVEN** the gateway not running in anonymous-browsing mode
- **WHEN** a request without a credential calls any endpoint subject to authentication
- **THEN** the request is rejected as unauthenticated

### Requirement: Console Invalid Credential Downgrade On Content

A console content request presenting a credential that matches no access-profile entry SHALL be
handled as a request carrying no credential: on a read endpoint it SHALL be admitted as the
anonymous read-only principal when the anonymous-browsing mode is enabled, and it SHALL be
rejected on a mutating endpoint regardless of that mode. Such a content response SHALL carry
the credential-rejection header.

#### Scenario: Invalid Credential Served As Guest On Content Reads

- **GIVEN** the gateway running in anonymous-browsing mode
- **WHEN** a request presenting a credential that matches no profile entry calls a content read
  endpoint
- **THEN** the request is processed as the anonymous read-only principal
- **AND** the response carries the credential-rejection header

#### Scenario: Invalid Credential Rejected On Mutations

- **GIVEN** the gateway running in anonymous-browsing mode
- **WHEN** a request presenting a credential that matches no profile entry calls a mutating endpoint
- **THEN** the request is rejected as unauthenticated with a message stating that a token is required

### Requirement: Console Identity Endpoint Is Strict

The identity endpoint SHALL always reject a credential that matches no access-profile entry,
even when anonymous browsing is enabled, so that an explicit login attempt reports its
failure instead of resolving to a guest session.

#### Scenario: Identity Endpoint Never Downgrades

- **GIVEN** the gateway running in anonymous-browsing mode
- **WHEN** a request presenting a credential that matches no profile entry calls the identity
  endpoint
- **THEN** the request is rejected as unauthenticated with a message stating that no profile entry
  matches the credential

### Requirement: Anonymous Scope Independence

The gateway's anonymous-browsing setting and each managed daemon's anonymous-reader setting SHALL be
independent, each gating only its own surface, so that enabling anonymous access on one surface does
not enable it on the other.

#### Scenario: Scopes Are Independent

- **GIVEN** the gateway running in anonymous-browsing mode while a managed daemon does not allow
  anonymous readers
- **WHEN** the gateway relays a credential-less read to that daemon
- **THEN** the gateway's own anonymous read of console-local content succeeds while the daemon's
  anonymous-read capability remains governed solely by the daemon's own setting

### Requirement: Instance Visibility Filtering

The console gateway SHALL report only those managed instances whose upstream probe succeeds,
hiding instances that are unreachable or whose upstream rejects the gateway's own probe, and
SHALL recompute that set after a successful login. A credential rejection encountered while
fetching an instance's *content* SHALL NOT hide the instance; it SHALL be reported on that
instance's dashboard instead (see "Dashboard Access Denial Reporting").

#### Scenario: Inaccessible Instance Hidden

- **GIVEN** a managed instance whose upstream probe fails
- **WHEN** the instance list is requested
- **THEN** that instance is absent from the returned list

#### Scenario: Instance Appears After Login

- **GIVEN** an instance hidden from an anonymous caller
- **WHEN** the same gateway is queried with a credential that is accepted upstream
- **THEN** the instance is present in the returned list

#### Scenario: Content Rejection Does Not Hide Instance

- **GIVEN** a managed instance whose health probe succeeds but whose content endpoints reject
  the caller's credential
- **WHEN** the instance list is requested
- **THEN** the instance is present in the returned list

### Requirement: Upstream Credential Relay

The console gateway SHALL authenticate each upstream request with the console caller's own
credential when the caller is authenticated, and SHALL fall back to the configured per-instance
service credential (`instances[].token` / `token_env` / `token_file`) when the caller is
anonymous, so that a signed-in operator's upstream rights follow their own profile entry and
the daemon's anonymous-reader setting continues to govern anonymous content reads through the
console.

#### Scenario: Authenticated Caller Credential Relayed

- **GIVEN** a console session authenticated with a profile credential
- **WHEN** the session reads an instance's dashboard
- **THEN** the upstream request carries the caller's credential, not the service credential

#### Scenario: Anonymous Caller Uses Service Credential

- **GIVEN** an anonymous console session and a configured per-instance service credential
- **WHEN** the session reads an instance's dashboard
- **THEN** the upstream request carries the service credential

#### Scenario: Anonymous Caller Without Service Credential Sends None

- **GIVEN** an anonymous console session and no configured service credential
- **WHEN** the session reads an instance's dashboard
- **THEN** the upstream request carries no `Authorization` header

### Requirement: Dashboard Access Denial Reporting

When an instance's content fetch is rejected upstream with `401` or `403`, the console gateway
SHALL return the dashboard with a structured access-denied state and an empty station list
rather than a raw upstream error blob, and the web console SHALL render a permission notice in
that instance's pane — stating that the current session lacks permission — instead of failing
to render. The pane itself SHALL NOT carry its own login button; signing in remains the job of
the top-bar login control.

#### Scenario: Access Denial Reported Structurally

- **GIVEN** an instance whose upstream rejects the caller's credential on the dashboard summary
- **WHEN** the dashboard endpoint is called
- **THEN** the response reports an access-denied status and an empty station array

#### Scenario: Permission Notice Rendered

- **GIVEN** a dashboard response carrying the access-denied state
- **WHEN** the web console renders the instance pane
- **THEN** the pane shows a permission notice naming the instance
- **AND** the pane contains no login button; the top-bar login control remains the way to sign in
- **AND** no unhandled client-side error occurs

#### Scenario: Null Collections Never Crash The Pane

- **GIVEN** any dashboard, tree, or info response whose collection field is `null` or absent
- **WHEN** the web console renders it
- **THEN** the pane renders without a client-side error

### Requirement: Console Login Flow

The console login dialog SHALL accept a credential only, SHALL NOT accept a user-chosen role or
subject, and SHALL determine the effective identity from the server response to the submitted
credential.

#### Scenario: Login Sends Only Credential

- **GIVEN** an unauthenticated console session
- **WHEN** the operator submits the login dialog
- **THEN** only the credential is sent and no role or subject is supplied by the client

#### Scenario: Role Derived From Server

- **GIVEN** a login submission with a valid `operator` credential
- **WHEN** the identity response is received
- **THEN** the console session reflects the role reported by the server, not one chosen by the user

#### Scenario: Invalid Credential Rejected With Message

- **GIVEN** a login submission with an unknown credential
- **WHEN** the identity response is received
- **THEN** the console states that the credential was rejected and remains in the anonymous or
  previous session state

#### Scenario: Rejected Login Dialog Is Dismissible

- **GIVEN** a login submission that was rejected
- **WHEN** the operator dismisses the login dialog
- **THEN** the dialog closes
- **AND** the console continues in its previous session state

### Requirement: Anonymous Console Session

When the gateway is running in anonymous-browsing mode, the console SHALL open directly into a
read-only session over the visible instances, SHALL offer a login control to upgrade in place, and
SHALL NOT present the login dialog as a mandatory first screen.

#### Scenario: Opens Without Login

- **GIVEN** a gateway running in anonymous-browsing mode
- **WHEN** the console is opened with no stored session
- **THEN** the dashboard is presented over the visible instances without requiring a login

#### Scenario: Login Upgrades In Place

- **GIVEN** an anonymous console session
- **WHEN** the operator logs in with an `operator` credential
- **THEN** the session is upgraded without a page reload and the newly visible instances and controls
  become available

#### Scenario: Logout Returns To Anonymous

- **GIVEN** an authenticated console session
- **WHEN** the operator logs out
- **THEN** the session returns to its anonymous or unauthenticated state and operator controls are
  removed

### Requirement: No Console User Management

The console gateway SHALL expose no capability to create, list, modify, or revoke access-profile
entries, so that access management remains a local, out-of-band operation.

#### Scenario: No Profile Mutation Endpoint

- **GIVEN** the gateway running with an access profile configured
- **WHEN** a request targets any console endpoint for creating, modifying, or revoking profile
  entries
- **THEN** no such endpoint is served and the request receives a not-found response

#### Scenario: Mutating Endpoint Does Not Write Profile

- **GIVEN** an operator-authenticated request to any console mutating endpoint
- **WHEN** the request completes
- **THEN** the access profile file is unchanged on disk

### Requirement: Console Page Title

The console webapp SHALL display a configurable page title as its top-bar heading and browser tab
title, sourced from the gateway configuration.

#### Scenario: Configured page title

- **GIVEN** gateway configuration with `ui.page_title` set
- **WHEN** an authenticated or anonymous-browsing user opens the console
- **THEN** the top-bar heading and the browser tab title render the configured value

#### Scenario: Default page title

- **GIVEN** gateway configuration without `ui.page_title`
- **WHEN** the configuration is loaded
- **THEN** the effective page title MUST be `VeriProc Operator Console`

#### Scenario: Page title served with instance list

- **GIVEN** the console gateway
- **WHEN** the frontend requests `GET /api/console/instances`
- **THEN** the response's `ui` object MUST include the effective `page_title` as a string

#### Scenario: Fallback before load

- **GIVEN** the webapp before the instance list has loaded or when the request is unavailable
  (e.g. the login screen of a gateway without anonymous browsing)
- **WHEN** the top bar and tab title are rendered
- **THEN** they display the built-in default `VeriProc Operator Console` until a `page_title`
  value is available
