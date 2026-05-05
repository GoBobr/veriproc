-- +migrate Up 0002 m5_m6
--
-- M5 + M6 schema extensions:
--   - runs.cancellation_requested_at  (Spec §5.8 cancellation)
--   - rolling_archive_publications    (Spec §4.3.12 / §4.6.6)
--   - deduplication_records           (Spec §4.3.15 canonical ownership)
--   - canonicality_audit              (Spec §3.16, §4.3.15: ownership history)
--   - api_credentials                 (M6 auth)
--   - rate_limit_buckets              (M6 quotas/rate limit)

ALTER TABLE runs ADD COLUMN cancellation_requested_at TIMESTAMP;

CREATE INDEX idx_runs_cancel_requested ON runs (cancellation_requested_at);

CREATE TABLE rolling_archive_publications (
    publication_id        TEXT PRIMARY KEY,
    artifact_id           TEXT NOT NULL,
    producing_run_id      TEXT,
    archive_id            TEXT NOT NULL,
    target_path           TEXT NOT NULL,
    publication_mode      TEXT NOT NULL,         -- copy | hardlink | symlink
    publication_state     TEXT NOT NULL,         -- pending | published | failed
    failure_reason        TEXT,
    created_at            TIMESTAMP NOT NULL,
    published_at          TIMESTAMP,
    UNIQUE (artifact_id, archive_id, target_path),
    FOREIGN KEY (artifact_id) REFERENCES artifacts(artifact_id),
    FOREIGN KEY (producing_run_id) REFERENCES runs(run_id)
);

CREATE INDEX idx_publications_artifact ON rolling_archive_publications (artifact_id);
CREATE INDEX idx_publications_run      ON rolling_archive_publications (producing_run_id);
CREATE INDEX idx_publications_state    ON rolling_archive_publications (publication_state);

CREATE TABLE deduplication_records (
    fingerprint_id        TEXT PRIMARY KEY,
    canonical_run_id      TEXT NOT NULL,
    superseded_by_run_id  TEXT,
    promotion_reason      TEXT,
    created_at            TIMESTAMP NOT NULL,
    promoted_at           TIMESTAMP,
    FOREIGN KEY (fingerprint_id) REFERENCES processing_fingerprints(fingerprint_id),
    FOREIGN KEY (canonical_run_id) REFERENCES runs(run_id)
);

CREATE INDEX idx_dedup_run ON deduplication_records (canonical_run_id);

CREATE TABLE canonicality_audit (
    audit_id              TEXT PRIMARY KEY,
    fingerprint_id        TEXT NOT NULL,
    previous_run_id       TEXT,
    new_run_id            TEXT NOT NULL,
    action                TEXT NOT NULL,         -- elect | promote | supersede
    reason                TEXT,
    actor                 TEXT,                  -- 'system' | api-key id
    occurred_at           TIMESTAMP NOT NULL,
    FOREIGN KEY (fingerprint_id) REFERENCES processing_fingerprints(fingerprint_id),
    FOREIGN KEY (new_run_id) REFERENCES runs(run_id)
);

CREATE INDEX idx_canonaudit_fp ON canonicality_audit (fingerprint_id);
CREATE INDEX idx_canonaudit_at ON canonicality_audit (occurred_at);

CREATE TABLE api_credentials (
    credential_id   TEXT PRIMARY KEY,
    token_hash      TEXT NOT NULL UNIQUE,    -- SHA-256 of bearer token
    subject         TEXT NOT NULL,           -- caller identity, e.g. operator name
    role            TEXT NOT NULL,           -- 'operator' | 'reader'
    quota_per_min   INTEGER NOT NULL DEFAULT 600,
    daily_submit_quota INTEGER NOT NULL DEFAULT 0,  -- 0 = unlimited
    created_at      TIMESTAMP NOT NULL,
    disabled_at     TIMESTAMP
);

CREATE TABLE rate_limit_buckets (
    credential_id TEXT NOT NULL,
    bucket_key    TEXT NOT NULL,            -- e.g. "submit" or "minute:202605051208"
    count         INTEGER NOT NULL DEFAULT 0,
    window_start  TIMESTAMP NOT NULL,
    PRIMARY KEY (credential_id, bucket_key)
);
