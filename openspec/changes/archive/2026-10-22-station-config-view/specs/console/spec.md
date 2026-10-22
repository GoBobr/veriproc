# Delta Spec for Capability: console

## ADDED Requirements

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

## MODIFIED Requirements

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