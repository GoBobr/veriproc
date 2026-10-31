-- Station retirement: marks all revisions of a station as retired so it
-- disappears from listings while run foreign keys stay intact. A live
-- re-upsert (station folder returns) clears the marker.
ALTER TABLE station_revisions ADD COLUMN retired_at TIMESTAMP NULL;
