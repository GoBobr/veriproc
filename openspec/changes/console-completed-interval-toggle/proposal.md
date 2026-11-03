# Proposal

## Why

Completed runs currently disappear from the dashboard slot rows exactly when `ui.completed_visibility_timeout` (default 30s, deployments commonly raise it to 1h) expires, regardless of which "since" interval the operator selected. Operators reviewing a processing day want to see everything that finished within the selected window (e.g. last 24h), while retaining the short linger time for live monitoring. Additionally, the daemon-side completed-slot age cutoff in `internal/stations` (`Service.SetCompletedVisibilityTimeout` and the `completedVisibilityTimeout` field) is dead code — never called anywhere in code, tests, or docs — and its continued existence falsely suggests the daemon participates in visibility pruning.

## What Changes

- Add a "completed" toggle (checkbox) next to the since date-time picker in each instance block of the console webapp. When checked, completed runs whose terminal timestamp falls within the selected since interval are shown, ignoring `ui.completed_visibility_timeout`.
- When `ui.completed_visibility_timeout` is explicitly set to a zero duration, the toggle is rendered disabled with a tooltip explaining that it is disabled by configuration (because no visibility pruning happens at all in that mode).
- Accept a `completed=1` query parameter on `GET /api/console/instances/{instance_id}/dashboard` that switches completed-slot pruning in `ExpandStationRow` from the visibility-timeout cutoff to the request's since cutoff.
- Operator-acknowledged (hidden) completed runs remain hidden even when the toggle is on; the toggle only overrides the age-based pruning.
- Remove the unreachable daemon-side completed-slot cutoff from `internal/stations/service.go` (the `SetCompletedVisibilityTimeout` method, the `completedVisibilityTimeout` field, and the cutoff logic in `summaryForStation`), aligning the code with the actual behavior: the upstream returns completed runs unpruned and the console gateway owns presentation pruning.
- Update `openspec/specs/console` requirements that reference upstream-side visibility pruning, and refresh the human-facing console documentation (`docs/docs/10-web-console.md`).

## Capabilities

### New Capabilities

(none)

### Modified Capabilities

- `console`: dashboard instance blocks gain a completed-in-interval toggle; slot rendering rules for completed runs gain the interval-based visibility mode; the station summary contract drops the assumption that the upstream applies a completed-run visibility cutoff; upstream capability testing requirements drop the dead upstream cutoff coverage item.

## Impact

- **Layers**: console (webapp frontend + `internal/console` gateway) and the `veriprocd` station-summary service (`internal/stations`). No REST API shape changes on `/api/v1`; one additive optional query parameter on the console dashboard endpoint. No CLI or executor changes.
- **Code**: `internal/console/dashboard.go` (`ExpandStationRow`, `BuildInstanceDashboard`), `internal/console/handlers.go` (`parseSince` area, dashboard handler), `webapp/src/api/client.ts`, `webapp/src/api/types.ts`, `webapp/src/components/InstanceBlock.tsx`, `webapp/src/pages/DashboardPage.tsx`, `internal/stations/service.go` (removal only).
- **Compatibility**: default behavior (toggle unchecked) is unchanged. Existing consumers of the dashboard endpoint are unaffected; the new query parameter is optional and additive.
- **Docs**: `docs/docs/10-web-console.md` config reference gains the zero-duration semantics of `completed_visibility_timeout` and the toggle description.
