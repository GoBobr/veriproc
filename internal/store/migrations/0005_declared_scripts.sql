-- Migration 0005: add declared_scripts column to station_revisions.
-- Stores a JSON-encoded map[string]string of script names → resolved absolute
-- paths (e.g. {"run": "/abs/path/to/scripts/run.sh"}). Empty string means
-- no scripts declared.
ALTER TABLE station_revisions ADD COLUMN declared_scripts TEXT NOT NULL DEFAULT '';
