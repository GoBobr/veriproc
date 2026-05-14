-- +migrate Up 0002 drop station_name unique index
--
-- The unique index on station_revisions.station_name prevented updating a
-- station's configuration: on restart with a changed station.yaml the new
-- content-hash produced a new revision_id, so ON CONFLICT(revision_id) never
-- fired, and the insert was rejected because the old revision row still held
-- the same station_name.
--
-- Cross-station name uniqueness (two different station_ids sharing a name) is
-- enforced at the application layer by Registry.Seed via the byName map.
-- Multiple historical revisions of the same station may therefore share the
-- same station_name in the DB without issue; runs keep their FK reference to
-- whichever revision_id was active when they were created.

DROP INDEX IF EXISTS uq_station_revisions_name;
