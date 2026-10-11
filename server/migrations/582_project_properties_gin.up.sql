-- Containment index for project custom-property value filtering (TCO-293).
-- Separate single-statement migration: CREATE INDEX CONCURRENTLY cannot run
-- inside a transaction or share a migration, and a non-concurrent build would
-- block writes on the hot project table for the duration of the scan.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_properties_gin
    ON project USING GIN (properties jsonb_path_ops);
