-- Rollback of 580: restore the workspace-wide unique name index.
-- Single-statement file because a concurrent build cannot share a migration.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_property_ws_name
    ON issue_property (workspace_id, LOWER(name));
