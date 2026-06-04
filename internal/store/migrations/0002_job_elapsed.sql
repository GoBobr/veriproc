-- Add elapsed_time to jobs to persist the scheduler-reported wall-clock
-- runtime (e.g. sacct Elapsed for Slurm: "[DD-]HH:MM:SS").
ALTER TABLE jobs ADD COLUMN elapsed_time TEXT;
