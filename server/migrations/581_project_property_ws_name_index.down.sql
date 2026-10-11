-- Rollback of 581; 580's down rebuilds the workspace-wide unique name index.
-- Single-statement file because a concurrent build cannot share a migration.
DROP INDEX CONCURRENTLY IF EXISTS idx_issue_property_ws_rt_name;
