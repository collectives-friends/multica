-- Enforce case-insensitive property-name uniqueness per resource type without
-- blocking writes. Replaces the workspace-wide idx_issue_property_ws_name that
-- 580 dropped. Keep this as the migration's only statement: PostgreSQL rejects
-- CREATE INDEX CONCURRENTLY inside a transaction or multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_property_ws_rt_name
    ON issue_property (workspace_id, resource_type, LOWER(name));
