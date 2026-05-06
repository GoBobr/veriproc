-- Migration 0007: record rolling archive source identity for resolved inputs.
ALTER TABLE resolved_input_entries ADD COLUMN source_archive_id TEXT;
CREATE INDEX idx_input_entries_archive ON resolved_input_entries (source_archive_id);
