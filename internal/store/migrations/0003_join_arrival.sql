-- Track how many upstream producers have contributed to a join task.
-- When join_arrival_count reaches the expected count the task is released.
ALTER TABLE tasks ADD COLUMN join_arrival_count INTEGER NOT NULL DEFAULT 0;
