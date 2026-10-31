# Design

## Context

Daemon startup (`internal/stations/loader.go`, `LoadDir`) upserts station
definitions found on disk into `station_revisions` via the registry (`Seed`),
but never removes rows for stations whose folders disappeared. Listings are
driven by `stations.Service.List()` → `store.Stations().ListCurrent()`
(newest revision per station_id), so ghost stations persist forever.
`runs.station_revision_id` has a hard FK to `station_revisions` (SQLite with
`foreign_keys(1)`), while `tasks.destination_station_id` is a plain TEXT
column with no FK. A complete task/run deletion cascade already exists in
`internal/cleaner` + `internal/store/cleanup.go`
(`PurgeTasks`/`PurgeRun`, working-root `os.RemoveAll`).

## Goals / Non-Goals

**Goals:**

- Retire stations without config folders via one CLI command or one command
  for all missing stations (`station remove` / `station prune`).
- Keep historical tasks/runs intact by default; optionally purge them.
- Surface drift (store rows without folders) at daemon startup via a log
  warning only.

**Non-Goals:**

- Live add/remove/rename of stations while the daemon runs; a daemon restart
  remains the mechanism for picking up station config folder changes.
- Automatic retirement or folder deletion at startup — the warning only
  tells the operator what to run.
- Renaming station_id in place (no data migration); rename = remove old +
  fresh load of new.
- Touching the console gateway code: retired stations vanish because
  dashboards render from the upstream station list.

## Decisions

1. **Retirement marker: `retired_at TIMESTAMP NULL` column on
   `station_revisions`** (new migration `000X_station_retirement`), set on
   *all* revision rows of the station in one UPDATE.
   - Why: keeps `runs.station_revision_id` FK targets intact (no row
     deletion, no nullable-FK migration), and retirement is then a pure
     filter in `ListCurrent` / `ExistsStationID` / `station_controls`
     lookups.
   - Alternative considered: a separate `retired_stations` table — rejected,
     extra join for the same information; deleting revision rows — rejected,
     blocked by the runs FK unless history is purged.
2. **Store API**: add `RetireStation(ctx, stationID)` (idempotent UPDATE +
     returning whether anything changed) and extend `ListCurrent` /
     `ExistsStationID` / any station-by-id resolution used for routing and
     pause/unpause with `retired_at IS NULL` filtering. Task/run history
     queries are untouched.
3. **Guard "still on disk"**: the daemon resolves retire requests against
   the in-memory registry (which mirrors disk). Retiring a station present
   in the registry → 409-style error. Unknown station → 404. Idempotent
   re-retire of an already-retired station → 200 no-op.
4. **HTTP**: `DELETE /api/v1/stations/{station_id}?purge=true|false` in
   `internal/httpapi/stations.go`, operator role, reusing the existing
   maintenance/clean plumbing for purge (same code path as
   `POST /api/v1/maintenance/clean` with `station_id` filter).
5. **CLI**: `veriproc station remove [--purge] <station_id>` (thin client,
   `--json` supported); `veriproc station prune [--purge] [--yes]` — lists
   the store-known stations lacking folders (fetched via a small API
   helper, see below), requires confirmation unless `--yes`.
   - Prune candidate discovery: the *daemon* is the only component that can
     compare disk vs store. Implement prune as: CLI asks the API for the
     missing-folder station list (one internal list field, e.g.
     `missing: true` flag on `GET /api/v1/stations` when `include_retired`
     or a dedicated `GET /api/v1/stations/missing`), then issues the same
     DELETE per station. Simplest concrete choice: dedicated lightweight
     `GET /api/v1/stations/missing` endpoint (operator role).
6. **Startup warning**: in `cmd/veriprocd/main.go` after `LoadDir`, compute
   `storeKnownStations − retiredStations − loadedStationIDs` and log one
   warning per missing station naming `veriproc station remove <id>`.
   Read-only, no writes. Retired stations are excluded so the warning does
   not fire forever for already-retired stations.
7. **`station_controls`**: rows for retired stations are left in place
   (harmless); retire removes nothing there. Pause/unpause of a retired
   station already fails through filtered lookup.

## Risks / Trade-offs

- [Retired station still referenced by other stations' `downstream:` blocks]
  → Task submission to a retired station returns the standard unknown-station
  error; startup validation of downstream targets reports it, guiding the
  operator to edit the YAML.
- [Ghost rows reappearing if the same folder returns] → Intentional: presence
  on disk restores the station; `Seed` should clear `retired_at` on re-upsert
  of a live station (one extra UPDATE in `Seed`).
- [Console `hidden_station_runs` rows for retired stations] → cosmetic,
  ignored; optionally swept later.
- [Purge deletes run data irreversibly] → Purge is opt-in only, and the
  existing dry-run/report semantics of the deletion cascade are reused where
  practical.

## Migration Plan

Single additive migration (new nullable column); safe to roll out with a
running daemon restart. Rollback: column is ignored by older code paths; no
data is transformed.

## Open Questions

- Exact response body shape of the DELETE endpoint and prune listing — can
  follow existing summary/dry-run report shapes during implementation
  without changing the specs.
