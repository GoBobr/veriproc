-- +migrate Up 0001 init
--
-- Initial schema for VeriProc M1 (core MVP, stub-executor profile).
-- Tables follow Spec Chapter 4. Indexes and constraints listed are the
-- minimum required to support M1–M6; later migrations may add denormalized
-- summary columns or additional secondary indexes.

CREATE TABLE station_revisions (
    revision_id     TEXT PRIMARY KEY,
    station_id      TEXT NOT NULL,
    content_hash    TEXT NOT NULL,
    schema_version  TEXT NOT NULL,
    label           TEXT,
    effective_at    TIMESTAMP NOT NULL,
    created_at      TIMESTAMP NOT NULL,
    UNIQUE (station_id, content_hash)
);

CREATE INDEX idx_station_revisions_station ON station_revisions (station_id);

CREATE TABLE idempotency_records (
    idempotency_record_id TEXT PRIMARY KEY,
    scope                 TEXT NOT NULL,
    key                   TEXT NOT NULL,
    request_hash          TEXT NOT NULL,
    task_id               TEXT,
    created_at            TIMESTAMP NOT NULL,
    expires_at            TIMESTAMP,
    UNIQUE (scope, key)
);

CREATE INDEX idx_idempotency_task ON idempotency_records (task_id);

CREATE TABLE tasks (
    task_id                  TEXT PRIMARY KEY,
    schema_version           TEXT NOT NULL,
    destination_station_id   TEXT,
    destination_proc_type    TEXT,
    window_start             TIMESTAMP NOT NULL,
    window_end               TIMESTAMP NOT NULL,
    force                    INTEGER NOT NULL DEFAULT 0,
    parent_task_id           TEXT,
    parent_run_id            TEXT,
    split_group_id           TEXT,
    priority                 TEXT,
    client_metadata          TEXT,        -- JSON-encoded
    routing_content          TEXT NOT NULL, -- canonical JSON snapshot
    routing_content_hash     TEXT NOT NULL,
    submission_origin        TEXT NOT NULL, -- 'client' | 'backend'
    state                    TEXT NOT NULL, -- 'accepted' | 'preparing' | ...
    failure_summary          TEXT,
    latest_run_id            TEXT,
    canonical_run_id         TEXT,
    idempotency_record_id    TEXT,
    created_at               TIMESTAMP NOT NULL,
    completed_at             TIMESTAMP,
    FOREIGN KEY (parent_task_id) REFERENCES tasks(task_id),
    FOREIGN KEY (idempotency_record_id) REFERENCES idempotency_records(idempotency_record_id)
);

CREATE INDEX idx_tasks_dest_station ON tasks (destination_station_id);
CREATE INDEX idx_tasks_proc_type    ON tasks (destination_proc_type);
CREATE INDEX idx_tasks_state        ON tasks (state);
CREATE INDEX idx_tasks_window       ON tasks (window_start, window_end);
CREATE INDEX idx_tasks_created_at   ON tasks (created_at DESC);
CREATE INDEX idx_tasks_parent_task  ON tasks (parent_task_id);
CREATE INDEX idx_tasks_parent_run   ON tasks (parent_run_id);
CREATE INDEX idx_tasks_split_group  ON tasks (split_group_id);
CREATE INDEX idx_tasks_canonical    ON tasks (canonical_run_id);

CREATE TABLE task_history_entries (
    task_id          TEXT NOT NULL,
    sequence_number  INTEGER NOT NULL,
    station_id       TEXT,
    ref_task_id      TEXT,
    ref_run_id       TEXT,
    summary          TEXT,
    completed_at     TIMESTAMP NOT NULL,
    PRIMARY KEY (task_id, sequence_number),
    FOREIGN KEY (task_id) REFERENCES tasks(task_id)
);

CREATE TABLE provenance_links (
    link_id            TEXT PRIMARY KEY,
    source_type        TEXT NOT NULL,
    source_id          TEXT NOT NULL,
    target_type        TEXT NOT NULL,
    target_id          TEXT NOT NULL,
    relationship_type  TEXT NOT NULL,
    role               TEXT,
    reason             TEXT,
    created_at         TIMESTAMP NOT NULL,
    UNIQUE (source_type, source_id, target_type, target_id, relationship_type)
);

CREATE INDEX idx_prov_source ON provenance_links (source_type, source_id);
CREATE INDEX idx_prov_target ON provenance_links (target_type, target_id);

CREATE TABLE processing_fingerprints (
    fingerprint_id    TEXT PRIMARY KEY,
    value             TEXT NOT NULL UNIQUE,
    canonical_run_id  TEXT,
    created_at        TIMESTAMP NOT NULL
);

CREATE TABLE runs (
    run_id                TEXT PRIMARY KEY,
    task_id               TEXT NOT NULL,
    station_revision_id   TEXT NOT NULL,
    retry_index           INTEGER NOT NULL,
    working_root          TEXT NOT NULL UNIQUE,
    state                 TEXT NOT NULL,        -- pending|preparing|...|complete|failed|cancelled
    canonicality          TEXT NOT NULL,        -- pending|canonical|non_canonical|duplicate|forced
    processing_fingerprint TEXT,
    failure_reason        TEXT,
    created_at            TIMESTAMP NOT NULL,
    prepared_at           TIMESTAMP,
    dispatched_at         TIMESTAMP,
    started_at            TIMESTAMP,
    terminal_at           TIMESTAMP,
    UNIQUE (task_id, retry_index),
    FOREIGN KEY (task_id) REFERENCES tasks(task_id),
    FOREIGN KEY (station_revision_id) REFERENCES station_revisions(revision_id)
);

CREATE INDEX idx_runs_task              ON runs (task_id);
CREATE INDEX idx_runs_station_revision  ON runs (station_revision_id);
CREATE INDEX idx_runs_state             ON runs (state);
CREATE INDEX idx_runs_fingerprint       ON runs (processing_fingerprint);
CREATE INDEX idx_runs_created_at        ON runs (created_at DESC);

CREATE TABLE jobs (
    job_id            TEXT PRIMARY KEY,
    run_id            TEXT NOT NULL,
    executor          TEXT NOT NULL,
    scheduler_id      TEXT,
    scheduler_state   TEXT,
    submission_attempt INTEGER NOT NULL DEFAULT 1,
    submitted_at      TIMESTAMP,
    last_observed_at  TIMESTAMP,
    terminal_at       TIMESTAMP,
    cancellation_requested_at TIMESTAMP,
    reconciliation_status TEXT,
    FOREIGN KEY (run_id) REFERENCES runs(run_id)
);

CREATE INDEX idx_jobs_run          ON jobs (run_id);
CREATE INDEX idx_jobs_scheduler_id ON jobs (scheduler_id);

CREATE TABLE resolved_input_manifests (
    manifest_id  TEXT PRIMARY KEY,
    run_id       TEXT NOT NULL UNIQUE,
    frozen_at    TIMESTAMP NOT NULL,
    FOREIGN KEY (run_id) REFERENCES runs(run_id)
);

CREATE TABLE resolved_input_entries (
    entry_id       TEXT PRIMARY KEY,
    manifest_id    TEXT NOT NULL,
    file_type      TEXT NOT NULL,
    category       TEXT,
    path           TEXT,
    optional       INTEGER NOT NULL DEFAULT 0,
    present        INTEGER NOT NULL DEFAULT 0,
    size           INTEGER,
    checksum       TEXT,
    checksum_algo  TEXT,
    FOREIGN KEY (manifest_id) REFERENCES resolved_input_manifests(manifest_id)
);

CREATE INDEX idx_input_entries_manifest ON resolved_input_entries (manifest_id);

CREATE TABLE artifacts (
    artifact_id        TEXT PRIMARY KEY,
    producing_run_id   TEXT,
    logical_type       TEXT NOT NULL, -- output | joborder | log | manifest_export | task_out
    file_type          TEXT,
    path               TEXT,
    size               INTEGER,
    checksum           TEXT,
    checksum_algo      TEXT,
    validation_status  TEXT,
    availability       TEXT,
    created_at         TIMESTAMP NOT NULL,
    FOREIGN KEY (producing_run_id) REFERENCES runs(run_id)
);

CREATE INDEX idx_artifacts_run ON artifacts (producing_run_id);
