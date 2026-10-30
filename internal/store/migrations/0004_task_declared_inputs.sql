-- Upstream-declared input payload for tasks created from task-out.yaml
-- `inputs:` entries: JSON array of {file_type, files}. Consumed by manifest
-- resolution for station inputs declared `source: upstream`. NULL for regular
-- tasks (windows/rolling matching as before).
ALTER TABLE tasks ADD COLUMN declared_inputs TEXT;
