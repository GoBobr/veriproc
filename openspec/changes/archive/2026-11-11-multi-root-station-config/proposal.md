# Proposal: multi-root-station-config

## Why

`storage.station_config_root` is a single directory. Deployments that share
one station set but need per-instance extras (e.g. e2e-vp: shared
`config/tds-gen/` plus instance-specific ghost stations in
`config/tds-gen-gaps/`) currently hack around this with docker-compose
bind-mount overlays into the shared root. Two painful consequences follow:

- YAML anchors do not merge volume lists, so every service needing the
  overlay repeats the whole anchor volumes block plus one mount line per
  ghost station.
- The overlay path exists only inside the daemon container. The
  slurm-docker executor resolves `execution.executable` on the compute node
  (plain shared-storage mount), so overlay ghost stations must hardcode
  absolute `executable` paths pointing at the real location instead of the
  plain relative `run.sh` used by regular stations.

Allowing multiple station config roots, with an optional per-root
station_id include filter, expresses the deployment shape directly in
instance.yaml and eliminates both hacks.

## What Changes

- **Config layer** (`internal/config`): new `storage.station_config_roots`
  list (`root` + optional `include`) alongside the legacy singular
  `station_config_root`. Empty roots after env expansion are dropped;
  explicit empty `include` means all stations in that root.
- **Station loading layer** (`internal/stations` loader, `cmd/veriprocd`):
  startup scans all configured roots, applies per-root include filters by
  `station_id`, and seeds one registry. Filtered-out stations never enter
  the registry (no drift complaints, no retirement-marker interaction):
  what is filtered out is simply not loaded, same as a folder that does
  not exist. A station_id duplicate across *loaded* stations from
  different roots remains a hard startup error. A configured root
  directory that does not exist is skipped with a warning, not an error.
- **Ordering**: `station_order` stays global; the directory-alphabetical
  fallback becomes per-root concatenated in root order (deterministic).
- **Docs**: `docs/docs/05-configuration.md`, `06-stations.md` updated.

Affected layers: instance configuration parsing (daemon config), station
loading at daemon startup, CLI/API view of stations indirectly (loaded
set changes per instance). REST API, CLI commands, console, and executors
are structurally unaffected. Affected existing capabilities:
`architecture` (daemon startup / station loading), and the retirement
capability `station-retirement` only insofar as filtered-out stations are
never counted as loaded (no spec change needed there — behavior follows
from "never enters the registry").

## Out of scope

- Per-root `exclude` lists (include only; exclude can be added later).
- Filtering by directory name (station_id only).
- Any change to executor executable resolution, compose files, or station
  retirement semantics.
- Support for nested/station roots defined via API or hot reload.
