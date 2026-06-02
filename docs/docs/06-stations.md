# 6. Stations & Job Orders

A **station** is a configured processing algorithm. You onboard one by writing a YAML file
under `storage.station_config_root`; the daemon loads all station files at startup and
tracks each as an immutable **revision** keyed by a content hash. This page documents the
station schema, context references, input resolution, and job-order rendering.

## 6.1 Where stations live

```text
sandbox/stations/
  station-a/station.yaml
  station-b/station.yaml
  …
```

Each station directory contains a `station.yaml` and may contain co-located resources
(scripts, schemas) referenced by the station. The directory is exposed to the job at
runtime as `VERIPROC_STATION_DIR` (see [Executors](07-executors.md)).

## 6.2 Station schema

A complete example (sandbox station A):

```yaml
station_id: statA
station_name: Scenario 2 ingest
schema_version: veriproc.station/v1

description: |
  Selects two rolling-archive inputs, writes a JSON output, publishes it back
  to proc-ra, and triggers statB.

execution:
  mode: local                 # executor type; falls back to executor.default if omitted
  executable: ./scripts/run.sh

joborder:
  format: yaml                # none | yaml | json | toml
  name: joborder.yaml         # output filename in the working root
  include:
    dyn_params:               # algorithm-facing variable params (root of the job order)
      mode: NOMINAL
      CpuCores: 12
    temporary_folder: ./temp
    config_files: [schemas]
    facility:
      processing_center: <facility.processing_center>   # context reference
      environment: <facility.environment>
      acquisition_station: <facility.acquisition_station>

inputs:
  - file_type: CLI_1B_RAD______
    category: product          # one of the configured product_categories
    window_match: overlaps     # none | overlaps | within_window | covers_window
    margins: [0, 0]            # [before, after] seconds added to the window
    mandatory: true
  - file_type: CO2_1A_GEO______
    category: product
    window_match: overlaps
    margins: [0, 0]
    mandatory: true
  - file_type: SCE_2__CAMF___AX
    category: aux
    object_kind: directory     # match a directory rather than a file
    window_match: overlaps
    margins: [0, 0]
    mandatory: true

outputs:
  - file_type: SCE_2__ICM______
    mandatory: true
    publish:
      rolling_archive: proc-ra # publish validated output back to this archive
      mode: copy               # copy | move | link

downstream:
  - station_id: statB          # route a downstream task on canonical success
```

### Field reference

| Section | Key | Meaning |
|---------|-----|---------|
| top | `station_id` | Unique station identifier referenced by tasks and `downstream`. |
| top | `station_name` | Human-readable name. |
| top | `schema_version` | Station schema identifier (`veriproc.station/v1`). |
| top | `description` | Free text. |
| `execution` | `mode` | Executor type to use (`local`, `stub`, `slurm-native`, `slurm-docker`). If omitted, `executor.default` is used. |
| `execution` | `executable` | Program/script to run, relative to the station directory or absolute. |
| `execution` | `args` | Optional argument list; context references are resolved per element. |
| `execution` | `resources` / `slurm` / `container` | Optional per-station execution hints for SLURM/Docker. |
| `joborder` | `format` | `none` (no file), `yaml`, `json`, or `toml`. |
| `joborder` | `name` | Output filename for the job order in the working root. |
| `joborder` | `include` | Station-provided fields placed at the **root** of the job order. |
| `joborder` | `meta` | When `false`, omit the generated `veriproc_meta` group from the file (audit is still recorded out of band). |
| `joborder` | `preprocess_script` | Optional path to an executable run **before** the job order is rendered. Its stdout (`KEY=VALUE` lines) is injected into the context as `<prep.KEY>` / `.PrepVars["KEY"]`. |
| `joborder` | `preprocess_args` | Argument list passed to `preprocess_script`; supports `{input:FILE_TYPE}` tokens and context references. |
| `inputs[]` | `file_type` | The product/aux file type to resolve. |
| `inputs[]` | `category` | Which `product_categories` entry to search. |
| `inputs[]` | `window_match` | Selection rule (see below). |
| `inputs[]` | `margins` | `[before, after]` seconds widening the match window. |
| `inputs[]` | `mandatory` | If true, the run fails when no match is found. |
| `inputs[]` | `object_kind` | `file` (default) or `directory`. |
| `inputs[]` | `filter` | Optional list of filter rules applied after window matching (see below). |
| `outputs[]` | `file_type` | Declared output type, validated after execution. |
| `outputs[]` | `mandatory` | If true, the run fails if the output is missing. |
| `outputs[]` | `publish` | Optional: `rolling_archive` target and `mode` (`copy`/`move`/`link`). |
| `downstream[]` | `station_id` | Stations to route to on canonical success. |

### Input window-match rules

| Value | Selects inputs that… |
|-------|----------------------|
| `none` | are not time-filtered. |
| `overlaps` | overlap the (margin-adjusted) processing window. |
| `within_window` (a.k.a. `fully_within`) | fall fully inside the window. |
| `covers_window` | fully cover the window. |

Selection uses the classical filename pattern from `naming.filenames` (the `<TOKEN>`
template with `?`/`*` wildcards) to parse start/end times from candidate filenames, then
applies the window rule and the category folder order. Earlier folders in the category win.

### Input candidate filters

After window matching, each input may declare a `filter:` list. Filters are applied in
order; a candidate must pass every rule to survive.

**`filename_component` rule** — keeps only candidates whose parsed value for the named
component matches the same component's value in the first already-resolved winner of a
`source_file_type` declared earlier in the same input list:

```yaml
inputs:
  - file_type: CO2_1A_GEO______     # resolved first
    category: product
    window_match: overlaps
    mandatory: true

  - file_type: AX_____MHF____AX
    category: aux
    window_match: overlaps
    mandatory: true
    filter:
      - rule: filename_component
        component: MISSION_ID        # keep only candidates whose MISSION_ID ...
        source_file_type: CO2_1A_GEO______  # ... matches the GEO winner's MISSION_ID
```

| Field | Required | Meaning |
|-------|----------|---------|
| `rule` | yes | Filter rule name. Currently only `filename_component` is supported. |
| `component` | for `filename_component` | Parsed filename component to compare (e.g. `MISSION_ID`). Case-insensitive. |
| `source_file_type` | for `filename_component` | `file_type` of the already-resolved input to take the reference value from. Must not be the same `file_type` as the filtered input itself. |

**Behaviour details:**

- Filters are applied _after_ window matching and _before_ interval grouping and winner
  selection.
- If the `source_file_type` has not yet been resolved at the time the filter runs (e.g.
  it appears later in the `inputs` list), the filter is silently skipped and all
  candidates survive.
- If every candidate is rejected by a filter rule, the input resolves to no winners and
  the reason message names the rule. A mandatory input then fails the run in `preparing`.

## 6.3 Context references

Station configuration values can reference the **station context** — a namespace built
from instance `definitions`, `execution_env`, the station's own config, and reserved
runtime keys. Two syntaxes:

| Form | Example | Behavior |
|------|---------|----------|
| **Full reference** | `<facility>` or `<facility.processing_center>` | Replaces the whole value, **preserving type** (scalar, map, or list). |
| **Embedded reference** | `out_<generic_mode>_v1` | Interpolated into a string; the reference **must resolve to a scalar**. |

References are resolved in:

- `execution.args` (each element),
- `joborder.include` (all leaves),
- and other string-valued station fields.

Reserved runtime keys available in the context include `station_id`, `station_name`,
`task_id`, `retry_index`, `run_ref`, `job_id`, `start`, `end`, `working_root`,
`instance_root`, and `joborder`. Do **not** define instance `definitions` with these
names — the daemon rejects that (see [Configuration §5.9](05-configuration.md)).

`instance_root` is the absolute path of the directory that contains the loaded
`instance.yaml` config file. It is particularly useful for referencing scripts or
auxiliary files that ship alongside the instance config:

```yaml
joborder:
  preprocess_script: <instance_root>/scripts/extract_meta.py
```

```yaml
# Instance definitions (instance.yaml)
definitions:
  facility: { processing_center: EUM, environment: OPE }
  generic_mode: NOMINAL

# Station uses them
execution:
  args: ["--center", "<facility.processing_center>", "--mode", "<generic_mode>"]
joborder:
  include:
    facility: <facility>          # full ref -> the whole map is inserted
    tag: "run-<task_id>"          # embedded ref -> scalar interpolation
```

## 6.4 Job-order rendering

The job order is the **only** orchestration-visible interface presented to the algorithm.
The system-default renderer supports `yaml`, `toml`, `json`, and `none`.

### Document layout (default renderer)

- **Root of the document:** workload-facing fields — `order`, `inputs`, `outputs` — plus
  station-declared fields from `joborder.include` (e.g. `dyn_params`, `log_level`).
- **`veriproc_meta` group:** VeriProc-owned audit and identity fields (schema version,
  station identity, task identity, retry index, run reference, manifest reference,
  generator identity). Set `joborder.meta: false` to omit this group from the file while
  keeping the audit record out of band.

`paths: relative` vs `absolute` (set under `generators.job_order`) controls whether input
and output paths in the job order are relative to the working root or absolute.

### Inputs in the job order

- Inputs are emitted as a single **flat list** when `file_type` is globally unique among
  the station's declared inputs.
- Each input entry may resolve to **one or many** filesystem objects of the same
  `file_type`, and an object may be a regular file or a directory.

### Job-order preprocess script

When `joborder.preprocess_script` is set, VeriProc executes the script (or any
executable) **before** rendering the job order. The script can inspect resolved input
files and print dynamic parameters to stdout; those are then available to the renderer
as context references.

#### How it works

1. The script is executed with the arguments from `preprocess_args` (after token and
   context-reference expansion).
2. Every line printed to **stdout** must be `KEY=VALUE` (blank lines and lines starting
   with `#` are ignored). Any other line format — or a non-zero exit code — immediately
   fails the run.
3. The extracted variables are available in the renderer's context under the `prep`
   namespace:
   - **Default renderer:** `<prep.KEY_NAME>` inside `joborder.include` values.
   - **Template renderer:** `{{ index .PrepVars "KEY_NAME" }}` inside the template.

#### `{input:FILE_TYPE}` token in `preprocess_args`

Each element of `preprocess_args` may contain one or more `{input:FILE_TYPE}` tokens.
Each token is replaced by the **space-joined absolute (or relative) paths** of all
present manifest entries of that file type. If no present entries match the given type,
the expansion fails and the run is aborted.

Context references (`<name.path>`) in `preprocess_args` and in `preprocess_script` are
resolved against the standard station context (same namespace as `execution.args`).

#### Example — default renderer

```yaml
joborder:
  format: yaml
  preprocess_script: <instance_root>/scripts/scan_range.sh
  preprocess_args:
    - "{input:SCE_DATA}"
  include:
    start_scanline: <prep.MIN_SCANLINE>
    end_scanline: <prep.MAX_SCANLINE>
    n_scanlines: <prep.N_SCANLINES>
    log_level: DEBUG
```

The script receives the resolved path to the `SCE_DATA` input file as `$1` and is
expected to print:

```text
MIN_SCANLINE=6825
MAX_SCANLINE=7444
N_SCANLINES=620
```

The rendered job order will contain the resolved values at the root.

#### Example — template renderer

```yaml
joborder:
  renderer: template
  format: yaml
  preprocess_script: <instance_root>/scripts/scan_range.sh
  preprocess_args:
    - "{input:SCE_DATA}"
  template: |
    start_scanline: {{ index .PrepVars "MIN_SCANLINE" }}
    end_scanline: {{ index .PrepVars "MAX_SCANLINE" }}
    run_ref: {{ .RunRef }}
```

#### Failure behaviour

| Condition | Result |
|-----------|--------|
| Script exits with non-zero code | Run transitions to `failed`; task is marked `failed`. |
| Stdout contains a line that is not `KEY=VALUE`, blank, or `#` comment | Run transitions to `failed`. |
| `{input:FILE_TYPE}` references a type with no present entries | Run transitions to `failed`. |

The failure message always names the script and the underlying cause.

### `format: none`

When `format` is `none`, no job-order file is written. The execution context is still
frozen by the resolved manifest plus the execution arguments and environment.

## 6.5 The working root at runtime

When the job runs, its working root contains:

```text
<working-root>/
  joborder.yaml          # generated job order (name from joborder.name)
  input/                 # symlinks to all resolved input objects
  output/                # algorithm writes outputs here
  logs/                  # stdout/stderr and run logs
  manifest.*             # frozen input selection
  task-out.*             # optional downstream routing descriptor written by the algorithm
```

The algorithm reads its inputs through `input/`, writes outputs and logs within the
working root, and may emit a task-output descriptor to drive downstream routing.

## 6.6 Downstream routing

When a run completes as **canonical**, the backend:

1. reads the optional task-output descriptor from the working root,
2. creates downstream tasks for each `downstream[]` entry (and any routes the algorithm
   declared),
3. carries the routing history forward and records provenance links,
4. optionally coordinates a split group for fan-in.

Duplicate and forced runs do **not** route downstream.

## 6.7 Onboarding checklist

1. Create `storage.station_config_root/<station>/station.yaml`.
2. Declare `inputs[]` against existing `product_categories`; confirm the categories and
   `rolling_archives` exist in the instance config.
3. Choose `execution.mode` and provide the `executable` (co-locate scripts in the station
   directory).
4. Pick a `joborder.format` and the `include` fields your algorithm expects.
5. Declare `outputs[]`, with `publish` if outputs should land back in a rolling archive.
6. Declare `downstream[]` if the station feeds others.
7. Restart the daemon (stations are loaded at startup) and verify with
   `veriproc station list`.
