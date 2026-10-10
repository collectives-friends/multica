CREATE UNIQUE INDEX CONCURRENTLY workspace_feature_flag_scope_idx ON workspace_feature_flag_override (workspace_id, scope_type, scope_id, flag_key);
