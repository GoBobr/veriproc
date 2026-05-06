-- Migration 0006: add station execution metadata columns.
-- These JSON-encoded columns keep the local MVP schema compact while preserving
-- station-declared inputs, downstream routes, publication policy, and category
-- folder overrides for run preparation and finalization.
ALTER TABLE station_revisions ADD COLUMN declared_inputs TEXT NOT NULL DEFAULT '';
ALTER TABLE station_revisions ADD COLUMN declared_downstream TEXT NOT NULL DEFAULT '';
ALTER TABLE station_revisions ADD COLUMN publication_policy TEXT NOT NULL DEFAULT '';
ALTER TABLE station_revisions ADD COLUMN rolling_folders TEXT NOT NULL DEFAULT '';
