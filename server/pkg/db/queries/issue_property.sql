-- name: ListIssueProperties :many
-- usage_count = number of resources in the workspace that currently carry a
-- value for this property, counted on the table its resource_type targets.
-- `properties ? id` is a seq scan today; fine at the 20-definition /
-- small-workspace scale this feature targets.
SELECT p.*,
    (
        CASE WHEN p.resource_type = 'project' THEN
            (SELECT COUNT(*) FROM project pr
             WHERE pr.workspace_id = p.workspace_id
               AND pr.properties ? p.id::text)
        ELSE
            (SELECT COUNT(*) FROM issue i
             WHERE i.workspace_id = p.workspace_id
               AND i.properties ? p.id::text)
        END
    )::bigint AS usage_count
FROM issue_property p
WHERE p.workspace_id = sqlc.arg('workspace_id')::uuid
  AND p.resource_type = sqlc.arg('resource_type')::text
  AND (sqlc.arg('include_archived')::bool OR p.archived_at IS NULL)
ORDER BY p.position ASC, LOWER(p.name) ASC;

-- name: GetIssueProperty :one
SELECT * FROM issue_property
WHERE id = $1 AND workspace_id = $2;

-- name: GetOwnedProjectPropertyByName :one
-- Plugin capability lookup: the installation's project-type definition with
-- this name. Uniqueness per (workspace, resource_type, name) makes this
-- deterministic without a separate key column.
SELECT * FROM issue_property
WHERE workspace_id = $1 AND resource_type = 'project'
  AND owner_installation_id = $2 AND LOWER(name) = LOWER(sqlc.arg('name')::text);

-- name: CountActiveIssueProperties :one
SELECT COUNT(*) FROM issue_property
WHERE workspace_id = $1 AND resource_type = $2 AND archived_at IS NULL;

-- name: CreateIssueProperty :one
-- New definitions append to the end of their resource type's catalog:
-- position = max + 1. owner_installation_id is only set by the plugin
-- capability, never by the human editor.
INSERT INTO issue_property (workspace_id, name, type, description, icon, config, position, resource_type, owner_installation_id)
SELECT sqlc.arg('workspace_id')::uuid,
       sqlc.arg('name')::text,
       sqlc.arg('type')::text,
       sqlc.arg('description')::text,
       sqlc.arg('icon')::text,
       sqlc.arg('config')::jsonb,
       COALESCE((SELECT MAX(position) FROM issue_property WHERE workspace_id = sqlc.arg('workspace_id')::uuid AND resource_type = sqlc.arg('resource_type')::text), 0) + 1,
       sqlc.arg('resource_type')::text,
       sqlc.narg('owner_installation_id')::uuid
RETURNING *;

-- name: UpdateIssueProperty :one
-- `type` is deliberately immutable — changing it would silently invalidate
-- existing values. archived_set/archived_at implement tri-state semantics:
-- archived_set=false leaves archived_at untouched.
UPDATE issue_property SET
    name = COALESCE(sqlc.narg('name'), name),
    description = COALESCE(sqlc.narg('description'), description),
    icon = COALESCE(sqlc.narg('icon'), icon),
    config = COALESCE(sqlc.narg('config'), config),
    archived_at = CASE WHEN sqlc.arg('archived_set')::bool THEN sqlc.narg('archived_at') ELSE archived_at END,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: SetIssuePropertyValue :one
-- Single-key atomic write: concurrent writers on different property keys
-- never clobber each other. Unlike SetIssueMetadataKey, a no-op still updates
-- and returns the issue row.
UPDATE issue
SET properties = jsonb_set(properties, ARRAY[sqlc.arg('key')::text], sqlc.arg('value')::jsonb, true),
    revision = revision + CASE WHEN properties -> sqlc.arg('key')::text IS DISTINCT FROM sqlc.arg('value')::jsonb THEN 1 ELSE 0 END,
    last_activity_at = CASE
        WHEN properties -> sqlc.arg('key')::text IS DISTINCT FROM sqlc.arg('value')::jsonb
        THEN GREATEST(COALESCE(last_activity_at, updated_at), now())
        ELSE last_activity_at
    END,
    updated_at = CASE WHEN properties -> sqlc.arg('key')::text IS DISTINCT FROM sqlc.arg('value')::jsonb THEN now() ELSE updated_at END
WHERE id = sqlc.arg('id')::uuid AND workspace_id = sqlc.arg('workspace_id')::uuid
RETURNING *;

-- name: DeleteIssuePropertyValue :one
UPDATE issue
SET properties = properties - sqlc.arg('key')::text,
    revision = revision + CASE WHEN properties ? sqlc.arg('key')::text THEN 1 ELSE 0 END,
    last_activity_at = CASE
        WHEN properties ? sqlc.arg('key')::text
        THEN GREATEST(COALESCE(last_activity_at, updated_at), now())
        ELSE last_activity_at
    END,
    updated_at = CASE WHEN properties ? sqlc.arg('key')::text THEN now() ELSE updated_at END
WHERE id = sqlc.arg('id')::uuid AND workspace_id = sqlc.arg('workspace_id')::uuid
RETURNING *;

-- name: CountIssuesUsingPropertyOptions :many
-- Usage census for specific option ids of one property. jsonb `?` matches
-- both value shapes: array element for multi_select, string equality for
-- select. Only options with at least one referencing issue come back.
SELECT opt::text AS option_id, COUNT(i.id) AS usage_count
FROM unnest(sqlc.arg('option_ids')::text[]) AS opt
LEFT JOIN issue i
  ON i.workspace_id = sqlc.arg('workspace_id')::uuid
 AND (i.properties -> sqlc.arg('property_key')::text) ? opt
GROUP BY opt
HAVING COUNT(i.id) > 0;

-- name: CountProjectsUsingPropertyOptions :many
-- Project-side counterpart of CountIssuesUsingPropertyOptions for
-- resource_type = 'project' definitions.
SELECT opt::text AS option_id, COUNT(pr.id) AS usage_count
FROM unnest(sqlc.arg('option_ids')::text[]) AS opt
LEFT JOIN project pr
  ON pr.workspace_id = sqlc.arg('workspace_id')::uuid
 AND (pr.properties -> sqlc.arg('property_key')::text) ? opt
GROUP BY opt
HAVING COUNT(pr.id) > 0;
