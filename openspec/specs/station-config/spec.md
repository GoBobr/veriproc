# station-config Specification

## Purpose
Loading stations at daemon startup from multiple station config roots,
with an optional per-root include filter by station_id, so deployments can
share one station set while enabling per-instance subsets of another root
without bind-mount overlays or absolute executable paths.

## Requirements

### Requirement: Multiple station config roots

The instance configuration SHALL support a list of station config roots
under `storage.station_config_roots`, each entry having a `root` path and
an optional `include` list of station IDs. At startup the daemon SHALL scan
every configured root and seed one shared station registry from the union.

#### Scenario: Stations from two roots are all loaded

- **GIVEN** an instance configuration listing root A containing station
  `co2gap` and root B containing station `cl2ref`
- **WHEN** the daemon starts up and loads stations
- **THEN** both `co2gap` and `cl2ref` are present in the station registry
  and visible in station listings

#### Scenario: Legacy singular root keeps working

- **GIVEN** an instance configuration using only the legacy singular
  `storage.station_config_root`
- **WHEN** the daemon starts up
- **THEN** stations load from that root exactly as before the change, with
  no filtering applied

#### Scenario: Singular root set alongside the list

- **GIVEN** an instance configuration setting both the legacy singular
  `storage.station_config_root` and `storage.station_config_roots`
- **WHEN** the daemon starts up
- **THEN** the singular root is loaded as one additional, unfiltered root
  entry and stations from all roots are registered

### Requirement: Per-root include filter by station ID

Each station config root entry SHALL support an `include` list of station
IDs. When `include` is present and non-empty, only stations whose
`station_id` appears in the list SHALL be loaded from that root. An absent
or empty `include` list SHALL load all stations from that root.

#### Scenario: Filtered station is invisible

- **GIVEN** root B contains stations `cl2gap` and `cl2ref` and its entry
  lists `include: [cl2ref]`
- **WHEN** the daemon starts up
- **THEN** `cl2ref` is loaded and `cl2gap` is absent from the registry and
  station listings, with no warning or error about `cl2gap`

#### Scenario: Filtered station triggers no guards

- **GIVEN** a station that a root's `include` filter excludes whose
  `station_id` also appears in another root or has historical store rows
- **WHEN** the daemon starts up
- **THEN** the filtered station does not trigger duplicate-detection
  against an identically-ID'd station in another root, does not count as
  loaded for retirement guards, and its exclusion emits no error or warning

#### Scenario: Same station in two roots, filtered in one

- **GIVEN** root A contains station `dup` and root B contains station
  `dup`, and root B lists `include` without `dup`
- **WHEN** the daemon starts up
- **THEN** `dup` loads exactly once, from root A, and startup succeeds

#### Scenario: Include entry matching nothing is not an error

- **GIVEN** a root entry listing `include: [nosuchstation]` and the root
  contains no station with that ID
- **WHEN** the daemon starts up
- **THEN** startup proceeds and the unmatched include entry does not fail
  loading

### Requirement: Duplicate station IDs across roots

When the same `station_id` would be loaded from more than one root after
filtering, the daemon SHALL fail startup with a duplicate-station error
naming the station ID and both source paths, preserving today's behavior
for duplicates within a single root.

#### Scenario: Unfiltered duplicate fails startup

- **GIVEN** root A and root B both contain a station with ID `dup` and
  neither entry filters it out
- **WHEN** the daemon starts up and loads stations
- **THEN** startup fails with an error naming station `dup` and both
  station config paths

### Requirement: Missing root skipped with warning

A configured station config root whose path does not exist on disk after
environment expansion SHALL be skipped with a log warning naming the path,
and startup SHALL continue with stations from the remaining roots. Roots
whose path expands to an empty string SHALL be dropped silently.

#### Scenario: Missing ghost root does not break startup

- **GIVEN** an instance configuration with root A (existing, containing
  stations) and root B (path does not exist on disk)
- **WHEN** the daemon starts up
- **THEN** stations from root A load normally, a warning naming root B's
  path is logged, and startup succeeds

#### Scenario: Empty root path after expansion is dropped

- **GIVEN** an instance configuration listing a root whose path expands to
  an empty string because its environment variable is undefined
- **WHEN** the daemon starts up
- **THEN** the entry is dropped without error, and no missing-directory
  warning is logged for it

### Requirement: Station order remains global

The `storage.station_order` setting SHALL order loaded stations globally
across all roots; when set it overrides any sort mode. Unlisted stations
SHALL be appended after the listed ones in station_id-alphabetical order.

#### Scenario: Explicit order spans roots

- **GIVEN** stations `alpha` loaded from root A and `beta` loaded from
  root B, and `storage.station_order` lists `beta` before `alpha`
- **WHEN** station listings are produced
- **THEN** `beta` appears before `alpha`

### Requirement: Station sort mode

When `storage.station_order` is not set, the fallback display order for
station listings SHALL be selected by `storage.station_sort`:
`folder_name` (the default) ranks stations by their config folder name
merged across all roots, with root membership never affecting ranking and
ties broken by station ID; `station_id` ranks stations globally by
station_id. An invalid value SHALL be a configuration error naming the
supported values.
#### Scenario: Fallback order is global folder-name order
- **GIVEN** root A (listed first) contains station `zeta` in folder
  `zeta-first` and root B contains station `alpha` in folder `a-alpha`,
  and no `storage.station_order` or `storage.station_sort` is set
- **WHEN** station listings are produced
- **THEN** `alpha` from root B precedes `zeta` from root A, because the
  default fallback ranks by station folder name regardless of which root
  the station was loaded from

#### Scenario: station_id sort mode

- **GIVEN** stations loaded from multiple roots and
  `storage.station_sort: station_id` with no `storage.station_order`
- **WHEN** station listings are produced
- **THEN** stations are ordered globally by station_id, independent of
  folder names and roots

#### Scenario: Invalid station_sort value is rejected

- **GIVEN** an instance configuration with `storage.station_sort: bogus`
- **WHEN** the configuration is loaded
- **THEN** loading fails with an error naming the invalid value and the
  supported values

#### Scenario: station_order overrides station_sort

- **GIVEN** both `storage.station_order` listing `beta` before `alpha` and
  `storage.station_sort: station_id`
- **WHEN** station listings are produced
- **THEN** `beta` precedes `alpha`, per the explicit order
