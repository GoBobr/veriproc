# station-retirement Specification

## Purpose
Retiring stations whose instance config folder no longer exists, so renamed
or deleted stations disappear from listings while their historical runs,
tasks, and revision records remain intact.

## Requirements

### Requirement: Station Retirement Semantics

The system SHALL support a retired state for a station persisted in the
store whose station config folder no longer exists on disk. A retired
station SHALL be excluded from station listings and from station lookups
used for new task routing and station control, while all historical tasks,
runs, and station revision records SHALL be preserved and remain
queryable. Implementation status: target behavior; today no retirement
mechanism exists and ghost stations remain listed forever after a rename.

#### Scenario: Retired station hidden from listings

- **GIVEN** the store contains station rows for station `oldname` and
  station `newname`, and only `newname` has a station config folder
- **WHEN** the store rows for `oldname` are marked retired
- **THEN** station listings and summaries expose `newname` only, and
  historical tasks and runs with destination station `oldname` remain
  retrievable by their identifiers

#### Scenario: Active station cannot be retired

- **GIVEN** station `active` has a station config folder loaded by the
  daemon
- **WHEN** a retirement of station `active` is requested
- **THEN** the request is rejected with an error naming the station and
  stating it is still present on disk

#### Scenario: Retirement is idempotent

- **GIVEN** station `oldname` rows are already marked retired
- **WHEN** a retirement of station `oldname` is requested again
- **THEN** the operation succeeds without changing any stored data

### Requirement: Station Retirement Endpoint

The REST API SHALL provide an operator-role endpoint that retires a
station by identifier, refusing the request when the station is still
present on disk. The endpoint SHALL support a purge option that first
deletes the station's tasks, runs, and working roots via the existing
deletion cascade before retiring, and SHALL report a deletion summary
when purging. Unknown station identifiers SHALL return a not-found error
consistent with the API error envelope.

#### Scenario: Retire via API keeps runs

- **GIVEN** station `oldname` is absent from disk and has historical runs
  in the store
- **WHEN** the operator calls the station retirement endpoint for
  `oldname` without the purge option
- **THEN** the response reports success for `oldname`, the station no
  longer appears in station listings, and its runs remain queryable

#### Scenario: Purge removes station data

- **GIVEN** station `oldname` is absent from disk and has tasks, runs, and
  working roots
- **WHEN** the operator calls the retirement endpoint for `oldname` with
  the purge option
- **THEN** the tasks and runs are deleted via the deletion cascade, the
  working roots are removed from the filesystem, and `oldname` no longer
  appears in station listings

#### Scenario: Unknown station rejected

- **GIVEN** no station named `nosuch` exists in the store
- **WHEN** the retirement endpoint is called for `nosuch`
- **THEN** the API responds with a not-found error using the standard
  error envelope

### Requirement: Station Remove CLI Command

The CLI SHALL provide a station remove command that retires one station by
identifier through the control-plane API, refusing when the station still
has a config folder on disk, and SHALL support a purge flag that cascades
deletion of the station's tasks, runs, and working roots before retirement.
The command SHALL follow the CLI exit code model and machine-readable
output modes.

#### Scenario: CLI remove retires ghost station

- **GIVEN** station `oldname` is present in the store but not on disk
- **WHEN** the operator runs the CLI station remove command for `oldname`
- **THEN** the command exits successfully, and subsequent station listings
  omit `oldname`

#### Scenario: CLI remove refuses active station

- **GIVEN** station `active` has a config folder loaded by the daemon
- **WHEN** the operator runs the CLI station remove command for `active`
- **THEN** the command exits with a nonzero exit code and prints an error
  naming the station

### Requirement: Station Prune CLI Command

The CLI SHALL provide a station prune command that identifies all stations
persisted in the store whose config folders are missing and retires them.
Before retiring, the command SHALL list the stations it would retire, and
it SHALL require explicit confirmation or an explicit force flag before
proceeding. The command SHALL support a purge flag with the same semantics
as station remove.

#### Scenario: Prune retires all missing-folder stations

- **GIVEN** stations `ghost1` and `ghost2` exist in the store without
  config folders, and station `live` has a config folder
- **WHEN** the operator runs the CLI station prune command with
  confirmation
- **THEN** `ghost1` and `ghost2` are retired, `live` remains listed, and
  historical tasks and runs of the pruned stations remain queryable when
  purge was not requested

#### Scenario: Prune without confirmation makes no changes

- **GIVEN** stations `ghost1` exists in the store without a config folder
- **WHEN** the operator runs the CLI station prune command without
  confirming
- **THEN** no station data is modified and the command reports the
  stations that would have been retired

### Requirement: Startup Drift Warning For Missing Stations

On daemon startup, for every station persisted in the store, not
retired, whose station config folder is missing from the instance station
config root, the daemon SHALL emit a log warning naming the station and
pointing at the CLI command that retires it. The daemon SHALL NOT create,
delete, or modify any station config folders or station store rows as part
of this check.

#### Scenario: Startup warns about ghost station

- **GIVEN** the store contains non-retired station `oldname` and the
  station config root has no folder for it
- **WHEN** the daemon starts
- **THEN** a warning naming `oldname` is logged mentioning the station
  remove command

#### Scenario: Startup does not warn about retired stations

- **GIVEN** the store contains station `gone` marked retired, and the
  station config root has no folder for it
- **WHEN** the daemon starts
- **THEN** no warning is logged for `gone`

#### Scenario: Startup makes no filesystem changes

- **GIVEN** the store contains stations without config folders
- **WHEN** the daemon starts
- **THEN** no station config folders are created and no station store rows
  are modified by the startup check
