CREATE INDEX CONCURRENTLY workspace_feature_flag_audit_idx ON workspace_feature_flag_audit (workspace_id, created_at DESC, id DESC);
