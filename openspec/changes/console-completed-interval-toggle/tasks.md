# Tasks

## 1. Daemon cleanup (upstream pruning independence)

- [x] 1.1 Remove the dead cutoff from `internal/stations/service.go` (`defaultCompletedVisibilityTimeout`, the `completedVisibilityTimeout` field, `SetCompletedVisibilityTimeout`, and the cutoff computation plus aged-out exclusion in `summaryForStation`) and verify with `grep -rn "CompletedVisibilityTimeout\|completedVisibilityTimeout" internal/` returning no matches — Station Summary Contract / Upstream pruning independence.
- [x] 1.2 Add or extend a stations service test asserting that completed runs are included in summary slots regardless of terminal age (fake clock far past terminal time), verifying Upstream Capability Testing's "completed runs never excluded by age" rule; run `go test ./internal/stations/...`.
- [x] 1.3 Run `go build ./... && go test ./...` to confirm no other package referenced the removed method.

## 2. Gateway: completed=1 dashboard mode

- [x] 2.1 In `internal/console/handlers.go`, parse the optional `completed=1` query parameter on `GET /api/console/instances/{instance_id}/dashboard` (truthy for `1`/`true`/`yes`, default false) and thread it plus `since` into `BuildInstanceDashboard` — Gateway Minimum API Surface / "Dashboard request carries the completed mode".
- [x] 2.2 Extend `ExpandStationRow` in `internal/console/dashboard.go` with the interval mode: when the flag is on, prune completed slots only when `terminal_at` precedes `since` instead of applying `CompletedVisibility`, keeping hidden-run suppression untouched — Completed Interval Toggle / "Toggle active shows interval completions".
- [x] 2.3 Add gateway unit tests covering: flag on shows completed older than the timeout but inside since; flag off keeps timeout pruning; missing `completed` parameter behaves as today; hidden acknowledged completed runs stay hidden with the flag on; completed slots without `terminal_at` are kept with the flag on. Verify with `go test ./internal/console/...` — Completed Interval Toggle scenarios.

## 3. Frontend: completed toggle

- [x] 3.1 Extend `ConsoleClient.dashboard` in `webapp/src/api/client.ts` to append `&completed=1` when requested, and extend the `DashboardInstance`/client types if needed; verify with `npm run build` in `webapp/`.
- [x] 3.2 Pass `completed_visibility_ms` from `DashboardPage` into `InstanceBlock` as a prop and add the "completed" checkbox next to the datetime picker: state persisted in the per-instance `localStorage` entry together with the since selection (default unchecked), wired to the dashboard fetch; disabled with tooltip "disabled in config (completed_visibility_timeout: 0)" when `completed_visibility_ms === 0` — Multi-Instance Dashboard Grid / "Instance block contents", Completed Interval Toggle / "Zero timeout disables the toggle".
- [x] 3.3 Add webapp tests in `webapp/tests/` covering: toggle checked sends `completed=1` on refresh and shows aged-out-but-in-interval completions; toggle unchecked hides them; checkbox disabled with explanatory title when `completed_visibility_ms` is 0; persisted state survives remount. Verify with `npm test` in `webapp/` — Completed Interval Toggle frontend scenarios.
- [x] 3.4 Add minimal styling for the checkbox in `webapp/src/styles.css` consistent with the existing preset-row controls; verify visually in the sandbox console (`sandbox-console/`).

## 4. Documentation and integration

- [x] 4.1 Update `docs/docs/10-web-console.md`: document the `completed` dashboard query parameter, the toggle behavior, and the zero-duration semantics of `ui.completed_visibility_timeout`; state the 200-run summary page bound for wide intervals. Verify the doc examples match the shipped config keys by running `go test ./docs/...`.
- [x] 4.2 Full verification: `go build ./...`, `go vet ./...`, `go test ./...`, and `cd webapp && npm test && npm run build`, all green.
