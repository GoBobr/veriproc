# Design: multi-root-station-config

## Context

Station loading today: `cmd/veriprocd/main.go` calls
`stations.LoadDir(ctx, cfg.Storage.StationConfigRoot, registry, st, env)`
once. `LoadDir` (internal/stations/loader.go) scans `*/station.yaml` in
lexical order, expands `${VAR}` env references, resolves relative
`execution.executable` against the station directory (the path the daemon
actually read — this is what makes relative executables valid on compute
nodes once no bind-mount overlay redirects the daemon's view), rejects
duplicate station IDs, and seeds the registry + store.

## Goals / Non-Goals

**Goals**

1. Multiple roots, per-root include filter by station_id, minimal surface
   change, fully backwards compatible.
2. Filtered-out stations behave exactly like nonexistent folders (no
   registry entry, no drift/retirement interaction, no log noise).

**Non-Goals:** exclude lists, directory-name filtering, API/CLI surface
changes, hot reload, executor changes.

## Decisions

### D1: Config shape — list under `storage.station_config_roots`

```yaml
storage:
  station_config_root: <env:MY_STATION_CONFIG_ROOT>     # legacy, kept
  station_config_roots:
    - root: <env:MY_STATION_CONFIG_ROOT>
      include: <env:MY_STATION_INCLUDE>
    - root: <env:MY_STATION_GHOST_ROOT>
      include: <env:MY_STATION_GHOST_INCLUDE>
```

- New Go types in `internal/config`:
  `StorageConfig.StationConfigRoots []StationConfigRoot{Root string; Include []string}`.
- `include` accepts either a YAML list or a scalar (comma-separated via
  `<env:...>` expansion produces a scalar — so parse scalars by splitting
  on commas and trimming whitespace). Absent/empty → no filter.
- Normalization (per accepted decisions):
  - Entries with empty `root` after expansion are dropped silently.
  - If the legacy singular key is also set, it is appended as an extra
    unfiltered entry (deduped if identical path to an existing entry —
    keep-first).
  - Verification: `VERIPROC_STATION_CONFIG_ROOTS` env override is
    out of scope; only the singular env override remains.

### D2: Loader — per-root pass, one shared `seen` map

Keep `LoadDir` signature-compatible for existing tests; add
`LoadDirs(ctx, roots []RootSpec, registry, st, env)` where
`RootSpec{Dir string; Include map[string]bool}`. `main.go` switches to
`LoadDirs`. Implementation: loop roots in config order; per root build
the station file list with the existing `stationFiles()`; per station:
read + parse, apply include filter (`if len(include)>0 && !include[id] →
continue` — before duplicate detection and before seeding); track a
single `seen` map across all roots for duplicate errors; accumulate
specs. Ordering of the fallback list: per-root lexical, concatenated in
root order.

Singular `LoadDir(root, ...)` becomes a thin wrapper over `LoadDirs`
with one unfiltered root — existing callers/tests unaffected.

### D3: Filter semantics — "never registered" (user decision #2)

The filter is applied before `registry.Seed`, before duplicate detection,
and before spec accumulation. Consequences (all desired):

- A station that exists on disk but is filtered in every root shows up in
  the existing startup drift warning path only if it has store rows from
  a previous instance where it *was* loaded — and per user decision,
  filtered ≠ missing: since it never registers, drift warning logic
  ("store-known, non-retired, folder missing") does not fire because the
  check is based on loaded folder paths vs store rows. Verify this
  interaction in implementation; if a filtered-out station with store
  rows triggers the drift warning, re-check: the drift check uses
  `ListMissing`-style comparison against the registry, and registry
  absence + disk-folder presence is the case where the warning is
  legitimate for *renamed* stations. Decision: filtered stations are
  treated as absent, and the drift warning comparing store rows to
  *loaded registry contents* (not disk contents) stays untouched — a
  filtered station with prior store rows will appear as missing exactly
  like a renamed station, which is correct operator information, not
  noise. Document this in 06-stations.md.
- Retirement guard (`ErrStationStillLoaded`) triggers on registry
  membership only — filtered stations are automatically retirable, which
  is the sane behavior.

### D4: Missing root — skip with warning (user decision #3)

In `LoadDirs`: `os.Stat` on the root dir; `ErrNotExist` →
`logger.Warn()`, continue; other stat errors → hard error (today's
behavior via `stationFiles`). Empty-path entries are already dropped at
config normalization. Warnings are returned to `main.go` (callback or
returned slice) since the loader has no logger; simplest: `LoadDirs`
returns `(specs, warnings []string, err)` and main logs them.

### D5: `station_order` stays global (user decision #4)

`main.go` builds `loadedDirOrder` from the concatenated spec order
returned by `LoadDirs`. `SetStationOrder` unchanged.

### D6: env-driven include lists

`<env:MY_INCLUDE>` expands (strict envexpand) to either "" (no filter) or
a scalar like "cl2ref,cl2gap". Config parsing splits scalars on ','. An
env var containing only commas/whitespace means the empty set → no
filter (not "include nothing"), because the practical failure mode
(misconfigured var) must not silently disable a whole root's stations.

## Risks / Trade-offs

- **Comma-splitting** is a mild type-laxity in YAML (list-or-scalar); the
  alternative (env-only lists are impossible) is worse. Tests must cover
  both forms.
- **Singular + list both set**: appending the singular as an extra root
  could duplicate an identical path; dedup by path (keep-first, merge
  include by union) avoids spurious duplicate-station startup errors.
- **Drift-warning interaction** (D3): a station intentionally filtered
  everywhere after having run before will surface as "missing" in the
  startup drift warning. This is the same signal a renamed station
  produces; the operator retires it explicitly. This is desired
  observability, not noise.

## Migration Plan

Additive: new config key, legacy key untouched. No DB migration. No
executor or API changes. Ship, then migrate e2e-vp deployments at
leisure: remove compose overlay mounts, restore relative `executable:
run.sh` in ghost stations, set `MY_STATION_GHOST_ROOT` /
`MY_STATION_GHOST_INCLUDE` per instance env file.

## Open Questions

None blocking — all four behavioral decisions confirmed by the operator
(station_id filtering; invisible-when-filtered with no complaints;
skip-with-warning for missing roots; global station order).
