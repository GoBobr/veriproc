# 14. Data Model Reference (As-Built)

This document is the as-built reference for the VeriProc relational persistence layer. It condenses the physical, column-level schema required for each persisted record type (fields, uniqueness constraints, indexes) from the former normative relational-data-model specification chapter (source document removed; see git history; §4.3.* and the §4.12 example logical schema), together with per-table "Implementation status" notes describing where the current baseline in `internal/store` deviates from that target model.

The normative persistence behavior requirements (identity models, immutability, lifecycle, idempotency, cardinality, migration rules) live in `openspec/specs/persistence/spec.md`. This document is informative reference material.

## tasks

- **Primary key:** `task_id`
- **Required fields:** task_id; destination station_id; processing-window start/end; creation timestamp; force flag; routing content (or normalized equivalent); current task summary state; submission origin (client-submitted vs backend-created).
- **Optional fields:** parent task id; parent run task id + retry index; split-group id; priority; client metadata; idempotency record reference; latest run retry index; canonical run retry index; failure-summary projection; completion timestamp.
- **Uniqueness:** unique `task_id`; unique derived downstream-creation key when deterministic downstream ids are used; unique idempotency (scope, key) resolution to one task result.
- **Indexes:** destination station_id; window start/end; task state; creation timestamp; parent task id; parent run (task_id, retry_index); split-group id; canonical run retry index.
- **Retention:** audit records; no physical deletion while referenced by runs, provenance, artifacts, split groups, or deduplication records. Archival/tombstoning allowed if queryability and referential integrity are preserved.
- **Implementation status:** implemented as per the required model; downstream-creation idempotency is enforced at the application layer rather than by a derived-id uniqueness constraint.

## task_history_entries

- **Primary key:** implementation-defined `history_entry_id` or `(task_id, sequence_number)`.
- **Required fields:** task id; sequence number; traversed station/task identifier; event timestamp.
- **Optional fields:** task-scoped run reference (task_id + retry_index); summary text; source task id; source run reference.
- **Uniqueness:** unique `(task_id, sequence_number)`. Entries are immutable once inserted.
- **Indexes:** task_id; source run reference.
- **Implementation status:** routing history is stored inside the immutable task payload rather than as a separate table; the separate-table model is the target form.

## provenance_links / task_relationships

- **Primary key:** `provenance_link_id` or (source object, target object, relationship type).
- **Required fields:** source object type/id; target object type/id; relationship type; creation timestamp.
- **Optional fields:** contribution role; creation reason; split-group id; producing run reference; metadata payload.
- **Uniqueness:** (source, target, relationship type) must not duplicate unless distinguished by explicit role/version.
- **Indexes:** source object; target object; relationship type; split-group id.
- **Implementation status:** machine-queryable provenance is derivable from task parent/run references and split-group membership records; the polymorphic link table is the target form.

## station_revisions

- **Primary key:** `station_revision_id`.
- **Required fields:** station revision id; station id; station_name; station schema version; immutable content hash; publication/effective timestamp; validation status.
- **Optional fields:** source commit; label; full validated document; execution image identity; split-group policy summary; input/output definition summaries.
- **Uniqueness:** unique `station_revision_id`; unique `(station_id, content_hash)`; deployment-wide unique active `station_id` and `station_name`.
- **Indexes:** station id; station_name; content hash; effective timestamp.
- **Retention:** a revision referenced by a run stays queryable for that run's retention period.
- **Implementation status:** the implemented table has no `validation_status` column; the `station_name`, `content_hash`, and `effective_at` indexes and deployment-wide uniqueness of active station id/name beyond `(station_id, content_hash)` are not present (Known Issue).

## instance_configuration_revisions

- **Primary key:** `instance_configuration_revision_id`.
- **Required fields:** revision id; instance id; schema version; content hash; effective timestamp; validation status.
- **Optional fields:** full validated document; storage-root, naming, integrity, rolling-archive, product-category, facility summaries; generator references.
- **Uniqueness:** unique revision id; unique `(instance_id, content_hash)`.
- **Implementation status:** NOT IMPLEMENTED. Effective instance configuration is loaded at startup and is not frozen relationally per run. Retained as the target model; tracked as a Known Issue.

## generation_hook_identities

- **Primary key:** `generation_hook_identity_id`.
- **Required fields:** hook type (`working_root` / `joborder`); implementation type; module name or system-default marker; version or content hash.
- **Optional fields:** configuration payload hash; timeout policy; validation mode; instance configuration revision id.
- **Uniqueness:** `(hook_type, implementation_name, version_or_hash, configuration_hash)` when available.
- **Implementation status:** NOT IMPLEMENTED. Declared generator identities are recorded as opaque strings in run metadata only.

## runs

- **Primary key:** conceptual composite `(task_id, retry_index)`; surrogate allowed with enforced uniqueness of the composite.
- **Required fields:** task id; station revision id; current run state; retry index; working-root path once materialized; creation timestamp.
- **Optional fields:** internal unique run key; instance config revision id; working-root/joborder generator identities; station id, window start/end projections; canonical classification; active job id; job-order artifact id; joborder format/filename projection; execution-argument summary/hash; manifest id; scheduler-uncertainty marker; finalization summary; failure reason; lifecycle timestamps.
- **Uniqueness:** `(task_id, retry_index)`; `working_root_path` once assigned (opaque); internal surrogate key one-to-one with composite; retry-safe run creation key.
- **Indexes:** task id; `(task_id, retry_index)`; station revision id; run state; creation timestamp; processing fingerprint; canonical classification; working-root path; active job id.
- **Implementation status:** the implemented schema uses an internal surrogate `run_id` as the physical primary key, with unique constraints on `(task_id, retry_index)` and `working_root`; jobs, manifests, artifacts and publications reference runs by `run_id`. The composite key is preserved in API projections via the `run_ref` display form; the surrogate key is currently also the primary REST resource identifier (Known Issue). Implemented state vocabulary: `pending`, `preparing`, `ready`, `dispatched`, `running`, `finalizing`, `complete`, `failed`, `cancelled` — no `dispatching`/`orphaned` states. Canonical-classification and active-job indexes are absent. Generator identities are opaque strings in run metadata. Lifecycle (terminal-state immutability, no revival) is enforced in SQL (`runs_lifecycle`).

## jobs

- **Primary key:** `job_id`.
- **Required fields:** job id; run task id; run retry index; executor type; submission attempt number; submission timestamp; scheduler identifier once known or pending/unknown marker.
- **Optional fields:** scheduler-native state; queue/partition/resource class; account and QoS; effective executor mode (`slurm-native`/`slurm-docker`); effective resource request; scheduler accounting fields; native exit code/elapsed/node list/diagnostics; wrapper log artifact references; wrapper exit-code marker; payload hash; cancellation timestamps; terminal scheduler timestamp; last poll timestamp; reconciliation status.
- **Uniqueness:** unique `job_id`; `(executor_type, scheduler_identifier)` when known; at most one active non-terminal job per run (default policy); `(run_task_id, run_retry_index, submission_attempt_number)`.
- **Indexes:** run reference; scheduler id; scheduler-native state; reconciliation status; submission timestamp.
- **Implementation status:** the implemented table enforces neither `UNIQUE(executor, scheduler_id)` nor `UNIQUE(run_id, submission_attempt)`; `scheduler_id` has a plain index, and the at-most-one-active-job-per-run rule is not schema-enforced (Known Issue).

## resolved_input_manifests

- **Primary key:** `manifest_id`.
- **Required fields:** manifest id; run task id; run retry index; schema version; creation timestamp; freeze timestamp before dispatch; resolution policy identity.
- **Optional fields:** exported manifest artifact id; content hash; resolver version; validation summary.
- **Uniqueness:** unique `manifest_id`; at most one frozen manifest per run (default preparation model).
- **Indexes:** run reference; freeze timestamp; content hash.
- **Implementation status:** the implemented header records run, folder sources, and selection results but does not persist `schema_version`, creation timestamp, resolution-policy identity, or content hash. Manifest exports exist as `artifacts` rows of logical type `manifest_export` but are not linked back to the manifest header.

## resolved_input_entries

- **Primary key:** `manifest_entry_id` or `(manifest_id, declared_input_key, object_sequence)`.
- **Required fields:** manifest id; declared input key/sequence; file type; category; mandatory flag; selection outcome (`resolved`/`missing_optional`); source precedence or missing reason.
- **Optional fields:** source path; working-root input path; object kind (`regular_file`, `directory`); object sequence; source rolling archive id; resolved category folder source; size; mtime; checksum value/algorithm/source (`sidecar`, `upstream_metadata`, `external_metadata`, `computed_by_veriproc`); effective filename pattern; applied `window_match` policy; parsed mission/start/end/generation timestamps; parsed `<FILE_TYPE>` substitution; post-generation discriminator; logical interval grouping key; source folder priority; winner comparison metadata; version metadata; tie-break reason; contributing child task/run/artifact references.
- **Uniqueness:** one entry per declared station input + one row per selected object; `(manifest_id, declared_input_key, object_sequence)` unique when row-based.
- **Indexes:** manifest id; file type; category; source rolling archive id; producing artifact id; contributing child task id.
- **Implementation status:** implemented entries have no `declared_input_key` column (identity is `file_type`/`category`/`entry_id`); selection outcome is a boolean `present` flag rather than `resolved`/`missing_optional`; a single `path` column without source-vs-working-root distinction; `file_type`/`category` indexes absent. Missing optional inputs must still be recorded explicitly. Under the default integrity policy `available_only`, checksums stay nullable and the backend must not read full file contents or walk directories solely to compute digests. Structured-filename selection metadata (pattern, parsed components, interval key, priority, winner metadata) must be preserved when used.

## artifacts

- **Primary key:** `artifact_id`.
- **Required fields:** artifact id; producing run (task_id, retry_index) or external origin; logical artifact type; filesystem path; object kind (`regular_file`/`directory`); storage role/namespace; creation/discovery timestamp.
- **Optional fields:** file type; relative path in working root; size; checksum value/algorithm/source; media type; validation status/timestamp/summary; mandatory-output flag; publication status summary; content metadata.
- **Uniqueness:** unique `artifact_id`; unique `(producing_run_task_id, producing_run_retry_index, logical_artifact_type, path)`; exactly one job-order artifact per run for `yaml`/`toml`/`json` joborder formats; none when `joborder.format: none`.
- **Indexes:** producing run reference; logical type; object kind; file type; validation status; path; checksum value.
- **Retention:** retained at least as long as the producing run; byte removal leaves the record with a retention marker.
- **Implementation status:** no `UNIQUE(producing_run_id, logical_type, path)` constraint — duplicates are prevented by application logic only; `logical_type`, `object_kind`, `file_type`, `validation_status`, and `checksum` indexes plus storage-role and validation-timestamp columns are absent; the exactly-one-job-order-artifact rule is not schema-enforced (all Known Issues). Directory artifacts are recorded as one logical record rooted at the directory path, without recursive inspection or recursive checksums.

## rolling_archive_publications

- **Primary key:** `publication_id`.
- **Required fields:** publication id; source artifact id; producing run reference; rolling archive id; target path (relative to archive root); published object kind (inherited from source artifact); publication mode; publication state; creation/publication timestamp.
- **Optional fields:** checksum value/algorithm/source; target size; target relative path; failure reason; retry count; retention policy identity.
- **Uniqueness:** unique `(source_artifact_id, rolling_archive_id, target_path)`; `(rolling_archive_id, target_path)` unique unless a versioned-collision replacement policy is persisted.
- **Indexes:** source artifact id; producing run reference; archive id; publication state; target path.
- **Implementation status:** `UNIQUE(artifact_id, archive_id, target_path)` is enforced and `target_path` is stored relative to the archive root; `archive_id`/`target_path` indexes and `retry_count`/`retention_policy` columns are absent. The asynchronous publication worker resolves pending records against a single configured archive base (Known Issue). Directory publications identify one logical directory root; copy, move, or symlink to the root are permitted; no recursive hardlinking or per-child checksums required.

## split_groups

- **Primary key:** `group_id`.
- **Required fields (target):** group id; parent task id or parent run reference; parent processing window; group evaluation mode (`aggregation`/`completion_only`); grouped evaluation target/stage; membership basis; closure state; readiness state; grouped evaluation state; readiness policy identity; creation timestamp.
- **Optional fields (target):** expected child count; child-key derivation rule; dynamic membership predicate; timeout policy; partial-aggregation policy; window override; aggregation target; aggregation task id; closure/readiness/grouped-completion timestamps; failure reason; producing run reference.
- **Uniqueness:** unique `group_id`; no duplicate groups for the same declared group key from a canonical run; at most one aggregation task per group (default policy).
- **Indexes (target):** parent task/run; closure, readiness, grouped evaluation states; aggregation task id; evaluation target.
- **Implementation status:** the implemented table is a simplified coordination record: group id, expected member count, canonical/failed counters, and a single `state` column (default `open`) — the separate parent-window, evaluation-mode, closure/readiness/evaluation states, readiness-policy identity, failure reason, and timeout/partial-aggregation policies are not present (Known Issues).

## split_group_memberships

- **Primary key:** `membership_id` or `(group_id, child_key)`.
- **Required fields:** group id; child key or member sequence; child task id; membership state; creation timestamp.
- **Optional fields:** expected child window; contributing canonical run reference; contributed artifact selection summary; exclusion state/reason; readiness contribution state; completion timestamp; duplicate-handling metadata.
- **Uniqueness:** `(group_id, child_key)`; `(group_id, child_task_id)`; a child task in at most one active group unless a persisted multi-group policy exists.
- **Indexes:** group id; child task id; membership state; readiness contribution state; contributing canonical run reference.

## deduplication_records

- **Primary key:** processing fingerprint, or a `deduplication_record_id` with a unique fingerprint.
- **Required fields:** processing fingerprint; owning run reference; canonical status; creation timestamp.
- **Optional fields:** fingerprint algorithm version; superseded-by run reference; superseded/promotion timestamps; replacement reason.
- **Uniqueness:** at most one current canonical owner per fingerprint; a run must not own more than one canonical record absent an explicit multi-fingerprint policy.
- **Indexes:** fingerprint; owning run; canonical status; superseded-by run.
- **Implementation status:** the implemented table omits `canonical status`, superseded timestamp, and fingerprint algorithm version. Ownership history is delegated to the separate `canonicality_audit` table, which is a schema extra exceeding this specification.

## idempotency_records

- **Primary key:** `(scope, idempotency_key)` or generated id with unique `(scope, key)`.
- **Required fields:** scope; key; request fingerprint/canonical request hash; operation type; first-seen timestamp; result status.
- **Optional fields:** task id; run reference; response summary; expiration timestamp; conflict timestamp; failure summary.
- **Uniqueness:** unique `(scope, idempotency_key)`.
- **Indexes:** scope+key; request fingerprint; expiration; task id.
- **Implementation status:** implemented with scope, key, request hash, task reference, timestamps and expiry, `UNIQUE(scope, key)` and an expiry sweep; `operation_type` and `result status` columns and `request_hash`/`expires_at` indexes are omitted.

## processing_fingerprint fields/records

- **Storage:** either `runs.processing_fingerprint` columns (indexed, immutable after computation; populated before dispatch eligibility) or a separate record keyed by fingerprint value with owner reference constrained to one `(task_id, retry_index)`.
- **Required fields:** fingerprint value; algorithm version; owner run reference; computation timestamp.
- **Optional fields:** canonicalized fingerprint input document; component hashes; station revision, manifest, runtime-parameter, and generator-identity components.
- **Uniqueness:** exactly one fingerprint per prepared run; multiple runs may share a fingerprint with only one current canonical deduplication owner.
- **Indexes:** fingerprint; run reference; algorithm version.

## Schema extras present in the implementation

The following tables exist in the implemented store but exceed the target specification model:

- `station_controls` — operator controls over stations.
- `api_credentials` — console/API authentication credentials.
- `rate_limit_buckets` — API rate-limit accounting.
- `canonicality_audit` — canonicality ownership change audit, standing in for supersession/promotion history on `deduplication_records`.
- `join_arrival_count` — tracks independent upstream run contributions that coalesce onto a shared join-mode downstream task.
