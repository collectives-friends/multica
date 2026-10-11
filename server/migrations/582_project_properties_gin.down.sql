-- Rollback of 582.
-- Single-statement file because a concurrent build cannot share a migration.
DROP INDEX CONCURRENTLY IF EXISTS idx_project_properties_gin;
