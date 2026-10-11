-- name: CreateConnection :one
INSERT INTO connection (
    level, workspace_id, provider_key, name, environment, config, secret_mode, created_by, updated_by
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
RETURNING *;

-- name: GetConnection :one
SELECT * FROM connection WHERE id = $1;

-- name: GetWorkspaceConnection :one
SELECT * FROM connection WHERE workspace_id = $1 AND id = $2;

-- name: ListWorkspaceConnections :many
SELECT * FROM connection
WHERE workspace_id = $1
ORDER BY name ASC;

-- name: ListInstanceConnections :many
SELECT * FROM connection
WHERE level = 'instance'
ORDER BY name ASC;

-- name: UpdateConnection :one
-- The expected revision makes a stale form save a 409 instead of overwriting a
-- newer edit; no row comes back when the revision moved or the row is gone.
UPDATE connection
SET name = $3,
    environment = $4,
    config = $5,
    secret_mode = $6,
    revision = revision + 1,
    updated_by = $7,
    updated_at = now()
WHERE id = $1 AND revision = $2
RETURNING *;

-- name: SetConnectionStatus :one
UPDATE connection
SET status = $2,
    status_detail = $3,
    last_checked_at = $4,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetConnectionEnabled :one
UPDATE connection
SET enabled = $2,
    revision = revision + 1,
    updated_by = $3,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteConnectionBindings :exec
DELETE FROM connection_binding WHERE connection_id = $1;

-- name: DeleteConnectionSecrets :exec
DELETE FROM connection_secret WHERE connection_id = $1;

-- name: DeleteConnection :exec
DELETE FROM connection WHERE id = $1;

-- name: UpsertConnectionSecret :one
-- One row per (connection, key); a rotation replaces the value and bumps
-- updated_at, which is the only rotation history a consumer can observe.
INSERT INTO connection_secret (connection_id, key, ciphertext, reference)
VALUES ($1, $2, $3, $4)
ON CONFLICT (connection_id, key) DO UPDATE
SET ciphertext = EXCLUDED.ciphertext,
    reference = EXCLUDED.reference,
    updated_at = now()
RETURNING *;

-- name: GetConnectionSecret :one
SELECT * FROM connection_secret WHERE connection_id = $1 AND key = $2;

-- name: ListConnectionSecretKeys :many
-- Names and rotation timestamps only: a read path must never carry ciphertext.
SELECT key, updated_at FROM connection_secret
WHERE connection_id = $1
ORDER BY key ASC;

-- name: DeleteConnectionSecret :exec
DELETE FROM connection_secret WHERE connection_id = $1 AND key = $2;

-- name: CreateConnectionBinding :one
INSERT INTO connection_binding (connection_id, consumer_type, consumer_id, created_by)
VALUES ($1, $2, $3, $4)
ON CONFLICT (connection_id, consumer_type, consumer_id) DO UPDATE
SET enabled = TRUE,
    updated_at = now()
RETURNING *;

-- name: ListConnectionBindings :many
SELECT * FROM connection_binding
WHERE connection_id = $1
ORDER BY consumer_type ASC, created_at ASC;

-- name: ListConsumerConnectionBindings :many
SELECT * FROM connection_binding
WHERE consumer_type = $1 AND consumer_id = $2 AND enabled
ORDER BY created_at ASC;

-- name: SetConnectionBindingEnabled :execrows
UPDATE connection_binding
SET enabled = $4,
    updated_at = now()
WHERE connection_id = $1 AND consumer_type = $2 AND consumer_id = $3;

-- name: DeleteConnectionBinding :exec
DELETE FROM connection_binding
WHERE connection_id = $1 AND consumer_type = $2 AND consumer_id = $3;

-- name: DeleteWorkspaceConnectionData :exec
-- Workspace teardown. Instance connections are deployment-owned and survive;
-- only the workspace's own rows go. Bindings go first: they name a connection
-- that is about to stop existing, and a leftover binding would outlive the
-- consumer the workspace delete is also removing.
WITH workspace_connections AS MATERIALIZED (
    SELECT connection.id
    FROM connection
    WHERE connection.workspace_id = $1
),
deleted_bindings AS (
    DELETE FROM connection_binding
    WHERE connection_id IN (SELECT id FROM workspace_connections)
),
deleted_secrets AS (
    DELETE FROM connection_secret
    WHERE connection_id IN (SELECT id FROM workspace_connections)
)
DELETE FROM connection WHERE connection.workspace_id = $1;
