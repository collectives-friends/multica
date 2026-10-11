-- Project-level custom properties (TCO-293): a thin twin of issue.properties.
--
-- Two-part model, mirroring migration 191:
--   1. issue_property gains a resource_type discriminator ('issue' | 'project')
--      plus an optional owner_installation_id for integration-owned project
--      definitions. Definitions remain one workspace-level catalog, so the
--      same Settings > Properties surface manages both kinds.
--   2. project.properties — JSONB value bag on each project keyed by property
--      definition UUID, structurally a sibling of issue.properties. Values are
--      typed per the definition and validated in the handler.
--
-- Integration-owned definitions (owner_installation_id set) are written only
-- through the plugin capability, never through the human property editor.
-- No FK on owner_installation_id: ownership follows the application-managed
-- plugin lifecycle — revoking an installation tombstones its definitions
-- instead of cascading.

ALTER TABLE project ADD COLUMN properties JSONB NOT NULL DEFAULT '{}'::jsonb;

-- NOT VALID + VALIDATE keeps the ACCESS EXCLUSIVE lock instantaneous; the
-- validation pass then runs under SHARE UPDATE EXCLUSIVE without blocking
-- writes (same pattern as issue_properties_is_object in migration 191).
ALTER TABLE project ADD CONSTRAINT project_properties_is_object
    CHECK (jsonb_typeof(properties) = 'object') NOT VALID;
ALTER TABLE project VALIDATE CONSTRAINT project_properties_is_object;

ALTER TABLE project ADD CONSTRAINT project_properties_size_limit
    CHECK (pg_column_size(properties) <= 16384) NOT VALID;
ALTER TABLE project VALIDATE CONSTRAINT project_properties_size_limit;

ALTER TABLE issue_property ADD COLUMN resource_type TEXT NOT NULL DEFAULT 'issue'
    CHECK (resource_type IN ('issue', 'project'));

ALTER TABLE issue_property ADD COLUMN owner_installation_id UUID;
