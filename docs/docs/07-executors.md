# 7. Executors

An **executor** abstracts job submission, status polling, and cancellation across backends.
A station selects one through `execution.mode`; if it declares none, the instance
`executor.default` is used. This page documents the available executors, the runtime
environment they inject, and SLURM specifics.

## 7.1 The executor contract

Every executor implements a small interface:

| Operation | Purpose |
|-----------|---------|
| `Type()` | Returns the executor type name (`local`, `stub`, `slurm-native`, `slurm-docker`). |
| `Submit(JobDescription)` | Submit a job; returns a scheduler submission id. |
| `Poll(id)` | Return the current job status. |
| `Cancel(id)` | Request cancellation (optional; not all executors support it). |
| `SupportsCancellation()` | Whether `Cancel` is meaningful. |

The daemon builds a **registry** mapping type names to instances from the `executor`
config. The dispatcher looks up the right executor per run.

## 7.2 Available executors

### `local`

Runs the station executable as a local OS process. Intended for sandbox and single-host
development.

- Captures `stdout`/`stderr` to log artifacts under the working root
  (`logs/run_out.log`, `logs/run_err.log`).
- No cancellation support.
- No configuration required: `local: {}` (multi-executor form) or `type: local`.

### `stub`

An in-memory, deterministic backend that simulates the job lifecycle **without running any
script**. Useful for tests and for exercising orchestration without real algorithms.

### `slurm-native`

Submits jobs to a SLURM cluster; the station executable runs **directly** on the allocated
SLURM node. The daemon submits via the configured submit command, polls via the query
command, and reads an exit-code marker for reconciliation.

### `slurm-docker`

Like `slurm-native`, but the station executable runs **inside a Docker container** launched
by the SLURM node wrapper. Adds container mount/user configuration.

> The control-plane contract (task → run → job → job order → working root) is identical
> across executors, so a station can move from `local` to SLURM without changing its
> algorithm.

## 7.3 The job runtime environment

At submission the executor injects a fixed set of `VERIPROC_*` environment variables into
the job, plus any operator-defined `execution_env` variables.

| Variable | Value |
|----------|-------|
| `VERIPROC_WORKING_ROOT` | Absolute path of the run's working root. |
| `VERIPROC_TASK_ID` | Task identifier. |
| `VERIPROC_RETRY_INDEX` | Retry index (decimal). |
| `VERIPROC_RUN_REF` | `"<task_id>/r<retry_index>"`. |
| `VERIPROC_STATION_ID` | Station identifier. |
| `VERIPROC_JOBORDER_PATH` | Path to the generated job order (omitted when `format: none`). |
| `VERIPROC_WINDOW_START` | Window start, `YYYYMMDDTHHmmSS` UTC. |
| `VERIPROC_WINDOW_END` | Window end, `YYYYMMDDTHHmmSS` UTC. |
| `VERIPROC_STATION_DIR` | The station's config directory (co-located scripts/schemas). |
| *(operator-defined)* | Each `execution_env` key/value, **without** a `VERIPROC_` prefix. |

The `VERIPROC_` prefix is reserved; operator `execution_env` variables must not use it
(enforced at config validation).

A station script can therefore be written generically:

```bash
#!/usr/bin/env bash
set -euo pipefail
cd "$VERIPROC_WORKING_ROOT"
# read inputs from input/, the job order from $VERIPROC_JOBORDER_PATH
# write outputs to output/, logs to logs/
```

## 7.4 SLURM configuration

SLURM executors are configured per type under `executor.executors.<type>.slurm` (or at the
top level in the legacy form). Key fields:

| Field | Meaning |
|-------|---------|
| `connection.mode` | `ssh` (run scheduler commands over SSH) or `local` (commands available on the daemon host). |
| `connection.host` / `user` / `key_file` | SSH target and key when `mode: ssh`. |
| `account` / `partition` / `qos` | SLURM accounting and scheduling selectors. |
| `submit_command` / `query_command` / `cancel_command` | Override the scheduler commands (defaults: `sbatch`/`sacct`/`scancel`). |
| `poll_interval` | How often to poll job status. |
| `defaults.cpus_per_task` / `defaults.mem_gb` / `defaults.walltime` | Default resource requests. |

For `slurm-docker`, add a `docker` block:

| Field | Meaning |
|-------|---------|
| `default_mounts` | Bind mounts applied to every container, e.g. `"/data:/data"`. |
| `user` | Container user, e.g. `"1000:1000"`. |

```yaml
executor:
  default: slurm-native
  executors:
    slurm-native:
      slurm:
        connection: { mode: ssh, host: slurm-login.example.org, user: veriproc, key_file: /etc/veriproc/id_ed25519 }
        account: co2m
        partition: batch
        qos: default
        poll_interval: 15s
        defaults: { cpus_per_task: 4, mem_gb: 16, walltime: "01:00:00" }
```

### Shared filesystem requirement

SLURM execution assumes a **shared filesystem** visible to both the daemon and the compute
nodes: the working root written by the daemon must be readable/writable by the job, and the
outputs written by the job must be visible to the daemon for validation and publication.

## 7.5 Cancellation and recovery

- `local` and `stub` do not support cancellation; a cancel request is recorded but cannot
  signal the process.
- SLURM executors support cancellation via the configured cancel command.
- For recovery, executors/wrappers write an **exit-code marker** the reconciler reads when
  a run goes stale (no poll progress past a threshold). See
  [Operations §11.6](11-operations.md).

## 7.6 Choosing an executor

| Situation | Use |
|-----------|-----|
| Local development / demos | `local` |
| Orchestration tests without real algorithms | `stub` |
| HPC cluster, native binaries on nodes | `slurm-native` |
| HPC cluster, containerized algorithms | `slurm-docker` |
