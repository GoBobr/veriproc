# Proposal

## Why

When a station directory is renamed or removed from the instance station
config root, the persisted station rows survive: startup seeding upserts
whatever is on disk but never removes rows for stations whose folders no
longer exist. Both the old and new station then stay listed forever in
`veriproc station list`, the console dashboard, and the webapp, and there is
no CLI or API command to retire the ghost entry. Developing new processing
workflows involves frequent station renames, so this gap hurts daily
operations.

## What Changes

- Introduce a **station retirement** marker in the store: a retired station
  keeps its `station_revisions` history (all existing run foreign keys stay
  valid) but is excluded from station listings and lookups.
- New CLI command `veriproc station remove <station_id>` (plus
  `--purge` variant) to retire a station that no longer has a config folder.
- New REST API `DELETE /api/v1/stations/{station_id}` (operator role) backing
  the CLI command, with the same `--purge` option expressed as a query/body
  flag.
- Daemon startup warns (log line) for every station present in the store but
  whose config folder is missing, telling the operator which CLI command
  retires it.
- New CLI command `veriproc station prune` that retires *all* stations found
  in the store whose config folders are missing, after listing them.
- `--purge` semantics reuse the existing cleaner cascade (task/run/artifact/
  working-root deletion) before retiring, for throwaway dev stations.

## Capabilities

### New Capabilities
- `station-retirement`: retiring stations whose config folders no longer
  exist — store semantics, startup drift warning, REST endpoint, and CLI
  commands (`station remove`, `station prune`).

### Modified Capabilities
_This change intentionally routes all new requirements through the new
`station-retirement` capability. The listing-filter behavior it mandates is a
behavioral delta on `domain-model` (Station Entity visibility) and the new
endpoint/command extend `api` and `cli`, but the norms those specs already
state (thin-client CLI, stable command semantics, error envelopes) remain
authoritative and unchanged; no existing requirement text needs rewriting.

## Impact

- **REST API**: new `DELETE /api/v1/stations/{station_id}` endpoint
  (operator role); affects `internal/httpapi`.
- **CLI**: new `station remove` and `station prune` subcommands; affects
  `cmd/veriproc`.
- **Console/webapp**: indirectly affected — retired stations disappear from
  dashboard rows once listings filter them; no console code changes required.
- **Executor layer**: not affected.
- **Persistence**: new migration adding the retirement marker (tombstone)
  on station rows; affects `internal/store`, `internal/stations`
  (registry/listing paths).
- **Daemon**: startup drift-warning log in `cmd/veriprocd` /
  `internal/stations` loader path.
- Affected existing capabilities consumed (not modified): `api`, `cli`,
  `domain-model`, `persistence`.
