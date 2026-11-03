# Spec Delta

## ADDED Requirements

### Requirement: Completed Interval Toggle

Each dashboard instance block SHALL offer a completed-runs toggle next to the since date-time picker. When active, completed runs whose terminal timestamp falls within the selected since interval SHALL be shown in the slot rows, ignoring the configured completed-run visibility timeout; when inactive, the timeout applies unchanged.

#### Scenario: Toggle active shows interval completions

- **GIVEN** a station with completed runs whose terminal timestamps are older than the visibility timeout but within the selected since interval
- **WHEN** the completed-runs toggle is active
- **THEN** those completed runs appear in the station slot rows

#### Scenario: Toggle inactive keeps timeout pruning

- **GIVEN** the same station and interval
- **WHEN** the completed-runs toggle is inactive
- **THEN** completed runs older than the visibility timeout do not appear in the slot rows

#### Scenario: Dashboard request carries the completed mode

- **GIVEN** the completed-runs toggle is active
- **WHEN** the instance dashboard is requested
- **THEN** the request carries a completed flag and the gateway prunes completed slots only when their terminal timestamp precedes the request since time, instead of applying the visibility timeout

#### Scenario: Zero timeout disables the toggle

- **GIVEN** `ui.completed_visibility_timeout` is configured as a zero duration
- **WHEN** an instance block is rendered
- **THEN** the completed-runs toggle is disabled and shows a tooltip indicating it is disabled by configuration

#### Scenario: Hidden completions stay hidden

- **GIVEN** a completed run the operator has acknowledged and hidden
- **WHEN** the completed-runs toggle is active and the run's terminal timestamp is within the selected interval
- **THEN** the run still does not appear in the slot rows

## MODIFIED Requirements

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
  a completed-runs toggle as specified by the Completed Interval Toggle requirement, and a scrollable list of
  station rows
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
  disappear automatically after the backend observes successful terminal completion and the timeout expires,
  except while the completed-runs toggle is active, in which case they MUST remain visible until their
  terminal timestamp falls outside the selected interval

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
- `GET /api/console/instances/{instance_id}/dashboard?since=...&completed=...` (the `completed` flag is
  optional and additive; omitting it keeps the visibility-timeout behavior)
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

### Requirement: Station Summary Contract

For each station, the upstream or adapter layer SHALL expose station identifier, paused state, count of
currently running runs, count of currently queued or accepted tasks, an ordered list of visible slot
occupants before console-local hidden-run suppression, success and failure counters for a
caller-supplied interval, and a last-authoritative-refresh timestamp.

#### Scenario: Gateway merges hidden runs

- **GIVEN** an upstream station summary
- **WHEN** the gateway builds the dashboard response
- **THEN** it MUST merge the summary with its own hidden-run acknowledgment table before returning it
- **AND** completed runs MUST remain visible only until the configured completed-run visibility timeout
  expires, or, while the completed-runs toggle is active, until their terminal timestamp precedes the
  request since window
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

- **GIVEN** an upstream station summary
- **WHEN** completed runs age past any timeout configured on the gateway
- **THEN** the upstream MUST NOT exclude them from the summary slot list by age; all completed-slot pruning
  by visibility timeout or since window is a presentation responsibility owned solely by the console gateway

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
- station-summary aggregation for running, queued, completed, failed, and cancelled runs, with completed runs
  never excluded by age
- retry eligibility and retry endpoint behavior
- pause and unpause endpoint behavior and authorization enforcement
- run-list retrieval for a task with deterministic ordering
- stable task and retry identity in every response needed by the console

- the upstream test suite MUST NOT require a live scheduler or live browser
- it SHOULD use table-driven service tests, fake stores, and narrow HTTP handler tests
