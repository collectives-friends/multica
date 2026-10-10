-- name: ListWorkspaceFlagOverrides :many
SELECT * FROM workspace_feature_flag_override WHERE workspace_id = $1 AND scope_type = 'workspace' ORDER BY flag_key;

-- name: ListMemberFlagOverrides :many
SELECT * FROM workspace_feature_flag_override WHERE workspace_id = $1 AND scope_type = 'member' AND scope_id = $2 ORDER BY flag_key;

-- name: GetFlagOverride :one
SELECT * FROM workspace_feature_flag_override WHERE workspace_id = $1 AND scope_type = $2 AND scope_id = $3 AND flag_key = $4;

-- name: LockWorkspaceFlagWrites :exec
-- Also acquired by lifecycle and membership cleanup, before reading definitions.
SELECT pg_advisory_xact_lock(hashtextextended('workspace_flags:' || sqlc.arg(workspace_id)::text, 0));

-- name: UpsertFlagOverride :one
INSERT INTO workspace_feature_flag_override(workspace_id,scope_type,scope_id,flag_key,enabled,locked,updated_by_type,updated_by_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (workspace_id,scope_type,scope_id,flag_key)
DO UPDATE SET enabled=EXCLUDED.enabled, locked=EXCLUDED.locked, updated_by_type=EXCLUDED.updated_by_type, updated_by_id=EXCLUDED.updated_by_id, updated_at=now()
RETURNING *;

-- name: DeleteFlagOverride :exec
DELETE FROM workspace_feature_flag_override WHERE workspace_id = $1 AND scope_type = $2 AND scope_id = $3 AND flag_key = $4;

-- name: DeleteFlagOverridesByPrefix :many
DELETE FROM workspace_feature_flag_override WHERE workspace_id = $1 AND starts_with(flag_key, sqlc.arg(prefix)::text) RETURNING *;

-- name: ListFlagOverridesByPrefix :many
SELECT * FROM workspace_feature_flag_override WHERE workspace_id = $1 AND starts_with(flag_key, sqlc.arg(prefix)::text) ORDER BY flag_key, scope_type, scope_id;

-- name: DeleteMemberFlagOverrides :exec
DELETE FROM workspace_feature_flag_override WHERE workspace_id = $1 AND scope_type = 'member' AND scope_id = $2;

-- name: InsertFlagAudit :one
INSERT INTO workspace_feature_flag_audit(workspace_id,flag_key,scope_type,scope_id,action,old_enabled,old_locked,new_enabled,new_locked,actor_type,actor_id,reason)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING *;

-- name: ListFlagAudit :many
SELECT * FROM workspace_feature_flag_audit
WHERE workspace_id = sqlc.arg(workspace_id)
AND (sqlc.arg(flag_key)::text = '' OR flag_key = sqlc.arg(flag_key))
AND (sqlc.narg(before_time)::timestamptz IS NULL OR (created_at,id) < (sqlc.narg(before_time)::timestamptz,sqlc.narg(before_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);
