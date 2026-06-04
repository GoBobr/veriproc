# 5. Configuration Reference

`veriprocd` is configured by a single YAML file (the **instance configuration**), passed
with `--config` or `VERIPROC_CONFIG`. This page documents every option, its default, and
the override rules.

> The console gateway has its own configuration — see
> [Operator Web Console](10-web-console.md). Station YAML files are documented in
> [Stations & Job Orders](06-stations.md).

## 5.1 Precedence

Configuration is resolved lowest-to-highest:

1. **Built-in defaults**
2. **YAML file** (path via `--config` or `VERIPROC_CONFIG`)
3. **Environment variables** (`VERIPROC_*`)
4. **Command-line flags**

A later source overrides an earlier one only when it supplies a non-empty value.

### Daemon flags

| Flag | Overrides | Notes |
|------|-----------|-------|
| `--config <path>` | — | Path to the YAML file (or `VERIPROC_CONFIG`) |
| `--http-addr <host:port>` | `http.bind_addr` | |
| `--log-level <level>` | `log.level` | trace\|debug\|info\|warn\|error |
| `--log-format <fmt>` | `log.format` | json\|console |
| `--instance-id <id>` | `instance_id` | |
| `--db-dsn <dsn>` | `db.dsn` | |

### Environment overrides

| Variable | Overrides |
|----------|-----------|
| `VERIPROC_CONFIG` | config file path |
| `VERIPROC_INSTANCE_ID` | `instance_id` |
| `VERIPROC_HTTP_ADDR` | `http.bind_addr` |
| `VERIPROC_HTTP_READ_TIMEOUT` | `http.read_timeout` |
| `VERIPROC_HTTP_WRITE_TIMEOUT` | `http.write_timeout` |
| `VERIPROC_HTTP_SHUTDOWN_TIMEOUT` | `http.shutdown_timeout` |
| `VERIPROC_LOG_LEVEL` | `log.level` |
| `VERIPROC_LOG_FORMAT` | `log.format` |
| `VERIPROC_DB_DSN` | `db.dsn` |
| `VERIPROC_WORKING_ROOT_BASE` | `storage.working_root_base` |
| `VERIPROC_STATION_CONFIG_ROOT` | `storage.station_config_root` |
| `VERIPROC_EXECUTOR` | `executor.type` (legacy single-executor form) |

## 5.2 Environment variable expansion in YAML files

Any value in the instance configuration file — and in every `station.yaml` and its
associated job-order template file — may reference an OS / container environment variable
using `${VAR}` or `$VAR` syntax.  The daemon expands all references **before** the YAML
is parsed, so substitution works in any position: paths, DSNs, archive roots, plain
scalars, etc.

```yaml
# instance.yaml — all ${…} references are replaced at startup
db:
  dsn: sqlite:///vpdata/${MY_INSTANCE_SUBDIR}/veriproc.db

storage:
  working_root_base: ${MY_WORKING_ROOT_BASE}
  station_config_root: ${MY_STATION_CONFIG_ROOT}

rolling_archives:
  aux:
    path: ${MY_AUX_DIR}
  rolling-eucent:
    path: ${MY_ROLLING_ARCHIVE}
```

**Rules:**

| Rule | Detail |
|------|--------|
| Undefined → fatal error | Every referenced variable must exist in the process environment. An undefined variable causes a startup failure that lists all missing names. |
| `$$` → literal `$` | Use `$$` to embed a literal dollar sign in a YAML value. |
| Syntax | Both `${VAR}` and `$VAR` are accepted (standard shell expansion rules). |
| Station configs included | Expansion is applied to each `station.yaml` and its job-order template file using the same environment snapshot. |
| No interference with `<key>` | The `<key>` / `<key.subkey>` station context-reference syntax is resolved at run time, after YAML parsing — it is unaffected by env-var expansion. |

**Typical Docker Compose usage:**

```yaml
# docker-compose.yaml
services:
  veriprocd:
    environment:
      MY_DATA_ROOT:    /mnt/data
      MY_AUX_DIR:      /mnt/data/aux.v2
      MY_PRODUCT_DIR:  /mnt/data/products/EUcent/v3
      MY_ROLLING_ARCHIVE: /mnt/data/rolling
      MY_WORKING_ROOT_BASE: /mnt/data/working-roots
      MY_STATION_CONFIG_ROOT: /config/stations
```

```yaml
# instance.yaml  (mounted into the container)
rolling_archives:
  aux:
    path: ${MY_AUX_DIR}
    mode: read_only
    retention: keep-fixtures
  prods:
    path: ${MY_PRODUCT_DIR}
    mode: read_only
    retention: keep-fixtures
  rolling:
    path: ${MY_ROLLING_ARCHIVE}
    mode: read_write
    retention: keep-fixtures
```

## 5.3 Top-level structure

```yaml
schema_version: veriproc.instance/v1   # config schema identifier
instance_id: sandbox-local             # deployment instance identifier (required)

http:        { … }                     # REST server
log:         { … }                     # logging
db:          { … }                     # state store
storage:     { … }                     # filesystem roots
naming:      { … }                     # task-id, working-root, filename policy
integrity:   { … }                     # checksum / metadata policy
definitions: { … }                     # arbitrary context injected into stations
execution_env: { … }                   # extra env vars exported to jobs
facility:    { … }                     # facility metadata (key/value)
rolling_archives: { … }                # named archive roots
product_categories: [ … ]              # ordered input search lists
executor:    { … }                     # executor registry
generators:  { … }                     # job-order / working-root generator identity
```

## 5.4 `http` — REST server

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `bind_addr` | `host:port` | `127.0.0.1:8080` | Address the API listens on. Must be a valid host:port. |
| `read_timeout` | duration | `15s` | Max time to read a request. |
| `write_timeout` | duration | `30s` | Max time to write a response. |
| `shutdown_timeout` | duration | `15s` | Grace period for in-flight requests on shutdown. |

Durations use Go syntax (`500ms`, `30s`, `5m`). Timeouts must be non-negative.

```yaml
http:
  bind_addr: 0.0.0.0:8080
  read_timeout: 30s
  write_timeout: 30s
  shutdown_timeout: 15s
```

## 5.5 `log` — logging

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `level` | enum | `info` | `trace`\|`debug`\|`info`\|`warn`\|`error` |
| `format` | enum | `json` | `json` for machine ingest; `console` for human-readable color output |

Console format honors `NO_COLOR` and detects whether the output is a TTY. Requests carry a
correlation ID (`X-Correlation-ID`) that appears in log lines.

## 5.6 `db` — state store

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `dsn` | string | — | Data source name for the instance database. |

The supported scheme in this version is SQLite: `sqlite://<path>`. The store opens with
foreign keys and WAL enabled and a single writer connection.

```yaml
db:
  dsn: sqlite://sandbox/data/veriproc.db
```

In Docker, point the path at a writable volume, e.g. `sqlite:///data/veriproc.db`.

## 5.7 `storage` — filesystem roots

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `working_root_base` | path | — | Base directory under which per-run working roots are created. |
| `station_config_root` | path | — | Directory scanned for station YAML files at startup. |
| `station_order` | list | (alphabetical) | Optional display order of station IDs. Unlisted stations are appended alphabetically. |

```yaml
storage:
  working_root_base: sandbox/data/working-roots
  station_config_root: sandbox/stations
  station_order: [scen-clim, clim-l2, scene-l2]
```

## 5.8 `naming` — identity & path policy

Controls how task IDs, working-root paths, and filenames are generated.

### `naming.task_id_timestamp`

| Value | Meaning |
|-------|---------|
| `start` | Task ID embeds the window **start** timestamp. |
| `creation` | Task ID embeds the task **creation** timestamp. |

### `naming.working_root`

The working-root path is built from a template plus segment definitions.

| Key | Example | Meaning |
|-----|---------|---------|
| `path_template` | `"{task}-{run}"` or `"{station}/{task}/{run}"` | Layout under `working_root_base`. Validated at startup. |
| `station_segment` | `"{station_id}"` | Renders the `{station}` placeholder. |
| `task_segment` | `"{task_id}"` | Renders the `{task}` placeholder. |
| `run_segment` | `"r{retry_index}"` | Renders the `{run}` placeholder. |
| `collision_suffix` | `"-{short_run_id}"` | Appended if a generated path already exists. |

Example result: `working-roots/scen2-20250703T110000000-a7sf7f-r0`, or with a collision,
`…-r0-a7sf7f`.

### `naming.filenames`

Rules for sanitizing and validating filename segments, plus a classical filename pattern
used during input matching.

| Key | Example | Meaning |
|-----|---------|---------|
| `allowed_charset` | `"A-Z a-z 0-9 - _ ."` | Permitted characters in generated segments. |
| `replace_invalid` | `"_"` | Replacement for disallowed characters. |
| `max_segment_length` | `128` | Maximum length of a path segment. |
| `station_case` | `preserve` | Case handling for station segment. |
| `filename_pattern` | see below | Template describing product filenames for matching. |
| `components` | map | Per-token rules (pattern, length, charset, format). |

```yaml
naming:
  filenames:
    filename_pattern: "<MISSION_ID>_<FILE_TYPE>_??_<START_TIME>_<END_TIME>_<GENERATION_TIME>_*"
    components:
      MISSION_ID:      { pattern: "CDM?", length: 4, charset: ascii }
      FILE_TYPE:       { length: 16, charset: ascii }
      START_TIME:      { format: "YYYYMMDDTHHmmSS", length: 15 }
      END_TIME:        { format: "YYYYMMDDTHHmmSS", length: 15 }
      GENERATION_TIME: { format: "YYYYMMDDTHHmmSS", length: 15 }
```

The pattern uses `<TOKEN>` placeholders, `?` for single-character wildcards, and `*` for
trailing wildcards. See [Stations & Job Orders](06-stations.md) for how this drives input
selection.

## 5.9 `integrity` — checksum & metadata policy

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `checksum_policy` | enum | `available_only` | `available_only`: record checksums when present; `none`: never; `required`: fail if a required checksum is missing. |
| `allowed_algorithms` | list | `[sha256]` | Acceptable checksum algorithms. |
| `record_size_when_available` | bool | `true` | Persist file sizes when known. |
| `record_mtime_when_available` | bool | `true` | Persist modification times when known. |

## 5.10 `definitions` — station context

An arbitrary nested YAML mapping injected at the root of the **station context-reference
namespace**. Stations reference these values with `<name>` / `<name.path>` syntax (see
[Stations](06-stations.md)).

Top-level keys **must not** collide with reserved runtime names:
`station_id`, `station_name`, `task_id`, `retry_index`, `run_ref`, `job_id`, `start`,
`end`, `working_root`, `joborder`. The daemon rejects the config otherwise.

```yaml
definitions:
  facility:
    processing_center: SANDBOX
    environment: LOCAL
    acquisition_station: Testbed
  generic_mode: TEST
```

A station could then use `<facility.processing_center>` or `<generic_mode>` in its
arguments or job order.

## 5.11 `execution_env` — extra job environment

Custom environment variables exported into every job process, **in addition** to the
reserved `VERIPROC_*` runtime variables (see [Executors](07-executors.md)).

Rules:

- Names must be valid shell variable names (`[A-Za-z_][A-Za-z0-9_]*`).
- Names **must not** start with the reserved `VERIPROC_` prefix.

```yaml
execution_env:
  OUTPUT_VERSION: "20260531T000000"
  PROCESSING_MODE: NRT
```

## 5.12 `facility` — facility metadata

A flat string map of facility metadata available to stations and audit records.

```yaml
facility:
  processing_center: EUM
  environment: OPE
```

## 5.13 `rolling_archives` — named archive roots

A map of archive name → definition. Stations and product categories reference archives by
name with the `rolling:<name>` selector.

| Key | Meaning |
|-----|---------|
| `path` | Filesystem location of the archive root. |
| `retention` | Retention policy label for the archive. |
| `mode` | `read_write` or `read_only` access intent. |

```yaml
rolling_archives:
  aux:
    path: sandbox/rolling-archives/aux
    retention: keep-fixtures
    mode: read_write
  prods:
    path: sandbox/rolling-archives/prods
    retention: keep-fixtures
    mode: read_write
```

## 5.14 `product_categories` — input search lists

An **ordered** list of categories, each naming an ordered list of folders to search when
resolving inputs. Folders reference rolling archives via `rolling:<name>`. Order matters:
earlier folders win.

```yaml
product_categories:
  - name: product
    folders:
      - rolling:proc-ra
      - rolling:prods
  - name: aux
    folders:
      - rolling:aux
```

A station input declares its category; resolution scans that category's folders in order.

## 5.15 `executor` — execution backend(s)

Two equivalent forms are accepted.

### Multi-executor form (preferred)

Declare a registry of executors and a default. Stations pick an executor by setting
`execution.mode`; if a station declares none, the `default` is used.

```yaml
executor:
  default: slurm-native
  executors:
    local: {}
    stub: {}
    slurm-native:
      slurm:
        connection:
          mode: ssh            # ssh | local
          host: slurm-login.example.org
          user: veriproc
          key_file: /etc/veriproc/id_ed25519
        account: co2m
        partition: batch
        qos: default
        submit_command: sbatch
        query_command: sacct
        cancel_command: scancel
        poll_interval: 15s
        defaults:
          cpus_per_task: 4
          mem_gb: 16
          walltime: "01:00:00"
    slurm-docker:
      slurm: { connection: { mode: ssh, host: slurm-login.example.org, user: veriproc } }
      docker:
        default_mounts: ["/data:/data"]
        user: "1000:1000"
```

- `default` must name a key present in `executors`.
- Recognized types: `stub`, `local`, `slurm-native`, `slurm-docker`. SLURM types validate
  their connection/resource settings.

### Legacy single-executor form

A simpler shape for one executor; equivalent to a registry with a single entry whose
default is that type.

```yaml
executor:
  type: local          # local | stub | slurm-native | slurm-docker
  # slurm: { … }       # when type is a SLURM variant
  # docker: { … }
```

See [Executors](07-executors.md) for behavior, the runtime environment, and SLURM details.

## 5.16 `generators` — job-order & working-root identity

Records the generator identity used to build job orders and working roots, for audit and
for inclusion in fingerprints when output-affecting.

| Generator | Key | Meaning |
|-----------|-----|---------|
| `job_order` | `type` | `system-default` (built-in) — the default renderer family. |
| | `version` | Version label recorded in audit/job orders. |
| | `paths` | `relative` (default) or `absolute` — how input/output paths appear in the job order. |
| `working_root` | `type` | `system-default`. |
| | `version` | Version label. |

```yaml
generators:
  job_order:
    type: system-default
    version: local-mvp-v1
    paths: relative
  working_root:
    type: system-default
    version: local-mvp-v1
```

## 5.17 Complete annotated example

The bundled [`sandbox/instance.yaml`](../../sandbox/instance.yaml) is a complete, working
example. A condensed version:

```yaml
schema_version: veriproc.instance/v1
instance_id: sandbox-local

http:    { bind_addr: 127.0.0.1:8080 }
log:     { level: info, format: console }
db:      { dsn: sqlite://sandbox/data/veriproc.db }
storage:
  working_root_base: sandbox/data/working-roots
  station_config_root: sandbox/stations

naming:
  task_id_timestamp: start
  working_root:
    path_template: "{task}-{run}"
    station_segment: "{station_id}"
    task_segment: "{task_id}"
    run_segment: "r{retry_index}"
    collision_suffix: "-{short_run_id}"

integrity:
  checksum_policy: available_only
  allowed_algorithms: [sha256]

definitions:
  facility: { processing_center: SANDBOX, environment: LOCAL }
  generic_mode: TEST

rolling_archives:
  aux:   { path: sandbox/rolling-archives/aux,   retention: keep-fixtures, mode: read_write }
  prods: { path: sandbox/rolling-archives/prods, retention: keep-fixtures, mode: read_write }

product_categories:
  - { name: product, folders: [rolling:prods] }
  - { name: aux,     folders: [rolling:aux] }

executor:
  type: local

generators:
  job_order:    { type: system-default, version: local-mvp-v1, paths: relative }
  working_root: { type: system-default, version: local-mvp-v1 }
```

## 5.18 Validation behavior

The daemon validates configuration at startup and refuses to start on error. Common
failures:

- empty `instance_id`;
- a `definitions` key colliding with a reserved runtime name;
- an `execution_env` name that is empty, starts with `VERIPROC_`, or is not a valid shell
  variable name;
- an invalid `naming.task_id_timestamp`, `integrity.checksum_policy`, `log.level`, or
  `log.format`;
- a malformed `http.bind_addr` or a negative timeout;
- an invalid working-root `path_template`;
- an `executor.default` not present in `executor.executors`, or an unrecognized executor
  type.
