-- +migrate Up 0003 m7: split groups + reconciliation
--
-- Milestone 7 schema additions:
--   - split_groups               (Spec §3.11 / §5.5.7 split-group identity + state)
--   - split_group_members        (run membership in a split group)
--   - runs.reconciliation_started_at (Spec §3.13 / §7.8 reconciliation marker)

CREATE TABLE split_groups (
    split_group_id   TEXT PRIMARY KEY,
    label            TEXT,
    description      TEXT,
    state            TEXT NOT NULL DEFAULT 'open',  -- open | aggregating | complete | failed
    expected_members INTEGER,                       -- optional closure rule
    canonical_count  INTEGER NOT NULL DEFAULT 0,    -- denormalized aggregator output
    failed_count     INTEGER NOT NULL DEFAULT 0,
    summary          TEXT,                          -- free-form aggregator summary text
    created_at       TIMESTAMP NOT NULL,
    closed_at        TIMESTAMP,
    aggregated_at    TIMESTAMP
);

CREATE INDEX idx_split_groups_state ON split_groups (state);
CREATE INDEX idx_split_groups_created_at ON split_groups (created_at DESC);

CREATE TABLE split_group_members (
    split_group_id   TEXT NOT NULL,
    run_id           TEXT NOT NULL,
    task_id          TEXT NOT NULL,
    role             TEXT,                          -- optional contribution role
    added_at         TIMESTAMP NOT NULL,
    PRIMARY KEY (split_group_id, run_id),
    FOREIGN KEY (split_group_id) REFERENCES split_groups(split_group_id),
    FOREIGN KEY (run_id) REFERENCES runs(run_id),
    FOREIGN KEY (task_id) REFERENCES tasks(task_id)
);

CREATE INDEX idx_sgm_run  ON split_group_members (run_id);
CREATE INDEX idx_sgm_task ON split_group_members (task_id);

ALTER TABLE runs ADD COLUMN reconciliation_started_at TIMESTAMP;
CREATE INDEX idx_runs_reconciliation ON runs (reconciliation_started_at);
