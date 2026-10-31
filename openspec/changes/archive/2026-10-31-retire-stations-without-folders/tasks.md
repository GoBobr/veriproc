# Tasks

## 1. Store layer

- [x] 1.1 Add migration adding nullable `retired_at` timestamp column to `station_revisions`; verify with `go test ./internal/store/...` migration tests
- [x] 1.2 Implement `RetireStation(ctx, stationID)` (idempotent, sets `retired_at` on all revisions of the station) and `ListMissingFolderStations`-style store query listing station_ids distinct in `station_revisions`; verify with a new unit test in `internal/store` covering retire, idempotent re-retire, and listing (implements Station Retirement Semantics — "Retired station hidden from listings", "Retirement is idempotent")
- [x] 1.3 Filter retired stations out of `ListCurrent`, `ExistsStationID`, station-by-id resolution used for routing and pause/unpause, while keeping task/run history queries untouched; clear `retired_at` in `Seed` when a live station is re-upserted; verify with unit tests showing ghost station absent from listing but its runs still queryable (implements Station Retirement Semantics — "Retired station hidden from listings")

## 2. HTTP API

- [x] 2.1 Implement `DELETE /api/v1/stations/{station_id}` (operator role) with `purge` query flag: refuse stations present in the registry with a named error, 404 unknown station, 200 idempotent re-retire, purge path reusing the existing cleaner cascade (`CleanupFilter{StationID}` + `PurgeTasks` + working-root removal); verify with `internal/httpapi` handler tests covering keep-history, purge, active-station refusal, and unknown-station error envelope (implements Station Retirement Endpoint — all scenarios)
- [x] 2.2 Implement `GET /api/v1/stations/missing` (operator role) returning station_ids known to the store but not loaded from disk; verify with a handler test (supports Station Prune CLI Command — "Prune retires all missing-folder stations")

## 3. CLI

- [x] 3.1 Add `veriproc station remove [--purge] [--json] <station_id>` thin-client subcommand with correct exit codes; verify with `cmd/veriproc` tests using a stub API server: success, active-station refusal (nonzero exit, named error), unknown-station 404 (implements Station Remove CLI Command — all scenarios)
- [x] 3.2 Add `veriproc station prune [--purge] [--yes]` — fetch missing stations, list them, require confirmation unless `--yes`, then retire each via DELETE; verify with CLI tests: confirmed prune retires ghosts and leaves live stations, unconfirmed prune makes no requests (implements Station Prune CLI Command — both scenarios)

## 4. Daemon startup warning

- [x] 4.1 After `LoadDir` in `cmd/veriprocd/main.go`, log one warning per non-retired, store-known station without a config folder, naming the station and the `veriproc station remove <id>` command, making no filesystem or DB writes; verify with a test (loader/main-level) asserting the warning content, no warning for retired stations, and no side effects (implements Startup Drift Warning For Missing Stations — all scenarios)

## 5. Documentation and regression

- [x] 5.1 Update human-facing docs under `docs/docs/` (station lifecycle / CLI reference) describing retirement, rename procedure, and the startup warning; verify via `docs/docs_test.go` if it validates referenced files
- [x] 5.2 Run full `go test ./...` and confirm no regressions; manually exercise against an instance with a renamed station (old id listed before, gone after `station remove`/`prune`, runs still queryable)
- [x] 5.3 Run `openspec validate --change retire-stations-without-folders` and fix any spec-format issues
