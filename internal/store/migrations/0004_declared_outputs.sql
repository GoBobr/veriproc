-- Migration 0004: add declared_outputs column to station_revisions.
-- Stores a JSON-encoded list of output filenames declared in station.yaml.
-- Empty string / NULL means no outputs declared.
ALTER TABLE station_revisions ADD COLUMN declared_outputs TEXT NOT NULL DEFAULT '';
