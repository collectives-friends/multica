CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS plugin_project_operation_identity_idx ON plugin_project_operation (installation_id, operation_id);
