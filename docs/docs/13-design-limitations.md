# 13. Design Considerations & Limitations

This page explains *why* VeriProc is built the way it is, and what it deliberately does or
does not do in this version. It is the place to look before assuming a behavior.

## 13.1 Design principles

### Separate orchestration from science code
The backend owns routing, identity, persistence, deduplication, archive interaction, and
recovery. A station algorithm sees only a generated job order and an isolated working root.
It never needs to know about the API, the task graph, the scheduler protocol, or the state
store. This keeps algorithms portable and the control plane testable.

### Three kinds of identity
Routing (task), execution (run/job), and processing (fingerprint) identities are kept
distinct. Conflating them is the usual source of bugs in pipeline systems — e.g. treating a
retry as a duplicate, or treating a re-run with new inputs as the same work.

### Scientifically safe deduplication
Deduplication is based on a **processing fingerprint** over the output-affecting context
(station revision, resolved manifest, window, force flag, etc.), not on station + time
window alone. Any change that could affect outputs produces a distinct fingerprint, so
deduplication never hides scientifically different work. Canonicality (`canonical` /
`duplicate` / `forced`) is decided at finalize from this fingerprint.

### Strong execution isolation
Each run executes in a dedicated working root. Inputs are exposed as symlinks under
`input/`; outputs, logs, and the optional routing descriptor are written inside the working
root. This makes runs reproducible and auditable, and prevents cross-run interference.

### Durable, recoverable state
Control state lives in a database; artifact bytes live on the shared filesystem. The run
state machine uses conditional transitions, and the reconciler recovers stale runs from
executor exit markers — so a crash never duplicates canonical work.

### Executor portability
The control-plane contract is identical across `local`, `stub`, and SLURM executors. A
station can move from a laptop to an HPC cluster without changing its algorithm.

### Declarative configuration
Stations, naming, archives, and executors are described in YAML. Behavior is driven by
configuration rather than code wherever practical, which keeps onboarding a new station to
writing one file.

## 13.2 Operating assumptions

- **Shared filesystem.** The control plane and the compute backend must see the same
  filesystem. The daemon writes working roots and reads outputs; SLURM jobs read/write the
  same paths. This is fundamental and not optional for distributed execution.
- **Trusted-ish network for open mode.** With authentication unconfigured, the API is open.
  Only run that way on a trusted local network.
- **Single-writer SQLite.** The local store uses one writer connection with WAL. It is
  excellent for development and lightweight deployments, but is a single-node store.

## 13.3 Known limitations (this version)

- **Persistence backend.** The implemented store is SQLite. The store is a facade designed
  to accept other backends (e.g. Postgres), but that is not part of this version.
- **Single-node control plane.** A given instance is one `veriprocd` process backed by one
  SQLite database. There is no built-in horizontal scaling or HA of the daemon; scale by
  running multiple independent instances (the console can front several).
- **Cancellation depends on the executor.** `local` and `stub` do not support real
  cancellation — a cancel request is recorded but cannot stop a local process. SLURM
  executors do support cancellation.
- **Algorithms must follow the working-root contract.** Outputs/logs must be written inside
  the working root and outputs must match declared `file_type`s for validation and
  publication to work.
- **Input matching is filename/time-window based.** Selection relies on the classical
  `filename_pattern` and `window_match` rules; products whose names do not match the
  configured pattern will not be resolved.
- **Generators.** Only the `system-default` job-order/working-root generators (plus the
  built-in template renderer) are exercised here; deployment-provided custom generator
  modules are a spec-level extension point rather than a turnkey feature in this version.
- **Asynchronous publication.** Outputs may be published after a run reaches `complete`;
  consumers that watch a rolling archive should not assume publication is synchronous with
  run completion.

## 13.4 Operational gotchas

- **Stations load at startup.** Adding or editing a station YAML requires restarting the
  daemon for the new revision to be picked up.
- **Reserved context names.** Do not put reserved runtime keys (`task_id`, `run_ref`,
  `working_root`, …) into instance `definitions`; the daemon refuses to start.
- **`VERIPROC_` prefix is reserved.** Operator `execution_env` variables must not start with
  it.
- **Folder order matters.** In `product_categories`, earlier folders win during input
  resolution — order authoritative archives first.
- **Forced runs never route downstream.** Use `--force` for replays/diagnostics, not to
  re-drive a pipeline; promote a duplicate instead if you need its outputs to supersede.
- **Clean with `--dry-run` first.** Deletion removes working roots on disk as well as
  database rows.

## 13.5 Relationship to the specification

This documentation describes the system *as implemented* in this version. The normative
design baseline — including requirements marked Must/Should/May and extension points not
yet realized — lives in [`docs/specs/`](../specs/). When in doubt about intended future
behavior, read the spec; when in doubt about current behavior, trust this guide and the
code.

## 13.6 Where to go next

- Re-read [Concepts & Lifecycle](03-concepts.md) to internalize the run state machine and
  fingerprinting.
- Use the bundled `sandbox/` profile to exercise submit → run → finalize → downstream end
  to end.
- Onboard a real station with the checklist in [Stations §6.7](06-stations.md).
- For distributed execution, configure a SLURM executor per [Executors §7.4](07-executors.md)
  and confirm the shared-filesystem assumption holds.
