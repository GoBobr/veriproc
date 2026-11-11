# Tasks: multi-root-station-config

## 1. Config schema

- [ ] 1.1 Add `StationConfigRoots []StationConfigRoot` (fields `Root`,
  `Include`) to `StorageConfig` in `internal/config/config.go`; parse
  `include` as list-or-scalar (comma-split scalars, trim whitespace); add
  normalization: drop empty `root` entries, append+merge legacy singular
  `station_config_root` as extra root (dedupe by path, union include).
  (Requirement: Multiple station config roots)
- [ ] 1.2 Config tests: list form, scalar include form, empty include →
  no filter, legacy-only config unchanged, empty-root dropping, singular
  and list coexistence with identical path.
  (Requirement: Multiple station config roots; Per-root include filter
  by station ID; Missing root skipped with warning)

## 2. Loader

- [ ] 2.1 Add `LoadDirs` (roots with include filters) in
  `internal/stations/loader.go`; apply include by `station_id` before
  duplicate detection and before registry seeding; single cross-root
  `seen` map; missing root dir → skip + collect warning; other stat
  errors → hard error. Keep `LoadDir` as a one-root wrapper (existing
  tests untouched).
  (Requirement: Multiple station config roots; Per-root include filter
  by station ID; Duplicate station IDs across roots; Missing root
  skipped with warning)
- [ ] 2.2 Switch `cmd/veriprocd/main.go` to `LoadDirs`; log collected
  missing-root warnings; build `loadedDirOrder` from concatenated
  per-root lexical order.
  (Requirement: Missing root skipped with warning; Station order
  remains global)
- [ ] 2.3 Loader tests (new, internal/stations):
  - two roots, no filters → both stations loaded
    (Scenario: Stations from two roots are all loaded);
  - include filter excludes a station from registry/listings and logs
    no warning/error about it
    (Scenario: Filtered station is invisible);
  - duplicate across roots with one side filtered → loads once,
    succeeds
    (Scenario: Same station in two roots, filtered in one);
  - unfiltered duplicate across roots → duplicate error naming station
    and both paths
    (Scenario: Unfiltered duplicate fails startup);
  - include entry matching nothing → startup OK
    (Scenario: Include entry matching nothing is not an error);
  - missing root dir → warning, other root's stations load
    (Scenario: Missing ghost root does not break startup);
  - empty-expanded root entry dropped without warning
    (Scenario: Empty root path after expansion is dropped).
- [ ] 2.4 Ordering tests: explicit `station_order` spans roots; fallback
  order is per-root lexical concatenated in root order.
  (Requirement: Station order remains global; Scenarios: Explicit order
  spans roots, Fallback order is root order then lexical)
- [ ] 2.5 Drift/retirement interaction check: with a store row for a
  station that is now filtered everywhere, verify startup drift warning
  fires (like a renamed station) and retirement of the station remains
  possible (guard is registry-based). Add/adjust test if behavior
  differs. (Design D3; documents interaction with capability
  station-retirement)

## 3. Docs

- [ ] 3.1 Update `docs/docs/05-configuration.md`: document
  `station_config_roots` (list-or-scalar include, empty-root semantics,
  legacy singular equivalence) and the env-driven comma-separated
  include pattern.
- [ ] 3.2 Update `docs/docs/06-stations.md`: multi-root loading,
  filter-invisible semantics, note that a station filtered out after
  having run surfaces as missing like a renamed station and can be
  retired explicitly.

## 4. Verification

- [ ] 4.1 Run full `go test ./...`; ensure existing loader/config
  smoke tests pass unchanged (backwards compatibility).
- [ ] 4.2 Sandbox/e2e check: configure `sandbox/` instance with a second
  root + include filter, start daemon, confirm station listings and
  startup log (missing-root warning path included).
