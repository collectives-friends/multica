-- Connections v1.
--
-- A connection is one stored credential plus its non-secret configuration for
-- an external system (Attio, Google, Microsoft 365, Parallel, Crustdata, ...).
-- Plugins, agents and native modules consume a connection through a binding
-- instead of each installation holding its own copy of the same secret.
--
-- Providers themselves are code-defined built-ins in this version; the table
-- for plugin-declared providers lands with the contribution adapter that reads
-- them, so no half-empty registry exists in the meantime.
--
-- Two ownership levels exist from the first version:
--
--   - workspace: owned by one workspace, managed by its administrators.
--   - instance:  owned by the deployment (level='instance', workspace_id NULL),
--                seeded from deployment configuration, readable by every
--                workspace but not editable here.
--
-- Relationships are application-owned by repository policy: no foreign keys,
-- no cascades. Secrets live in their own table so no connection read path can
-- reach one by construction; values are encrypted with the deployment's
-- connection secret key and are never returned by any endpoint.

-- One row per connection. `revision` increments on every accepted mutation so a
-- consumer holding a stale read can be told its view is out of date; the
-- service compares it under a row lock.
CREATE TABLE connection (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    level          TEXT NOT NULL CHECK (level IN ('instance', 'workspace')),
    -- NULL exactly when level = 'instance'.
    workspace_id   UUID,
    provider_key   TEXT NOT NULL CHECK (char_length(provider_key) BETWEEN 1 AND 128),
    name           TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 160),
    environment    TEXT NOT NULL DEFAULT 'production' CHECK (environment IN ('production', 'fixture')),
    -- Non-secret configuration only, validated against the provider schema.
    config         JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(config) = 'object'),
    -- 'host' stores an encrypted value here. 'reference' stores only a pointer
    -- to an external secret store (for example Azure Key Vault), which the
    -- deployment operator owns and rotates.
    secret_mode    TEXT NOT NULL DEFAULT 'host' CHECK (secret_mode IN ('host', 'reference')),
    status         TEXT NOT NULL DEFAULT 'unchecked' CHECK (status IN ('unchecked', 'ok', 'degraded', 'error', 'needs_reauth')),
    status_detail  TEXT NOT NULL DEFAULT '' CHECK (char_length(status_detail) <= 1024),
    last_checked_at TIMESTAMPTZ,
    enabled        BOOLEAN NOT NULL DEFAULT TRUE,
    revision       BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_by     UUID,
    updated_by     UUID,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((level = 'workspace') = (workspace_id IS NOT NULL))
);

CREATE INDEX connection_workspace_idx ON connection (workspace_id) WHERE workspace_id IS NOT NULL;
CREATE INDEX connection_instance_idx ON connection (level) WHERE level = 'instance';
CREATE INDEX connection_provider_key_idx ON connection (provider_key);

-- Secrets live apart from `connection` so no list or detail read can return one
-- by construction. `ciphertext` is secretbox output; `reference` is the
-- operator-owned pointer used when secret_mode = 'reference' (the service
-- enforces which column belongs to which mode). Rotating a secret replaces the
-- row's ciphertext; a consumer reading at the wrong moment sees the new value
-- only after the transaction commits.
CREATE TABLE connection_secret (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id UUID NOT NULL,
    key           TEXT NOT NULL CHECK (char_length(key) BETWEEN 1 AND 128),
    ciphertext    BYTEA CHECK (ciphertext IS NULL OR octet_length(ciphertext) > 0),
    reference     TEXT NOT NULL DEFAULT '' CHECK (char_length(reference) <= 1024),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (connection_id, key)
);

-- A binding is the grant: which consumer (a plugin installation, an agent, or a
-- native module) may use this connection. Disabling one revokes the grant
-- without deleting the connection or its secret. Application-owned; uninstall
-- paths delete their own bindings.
CREATE TABLE connection_binding (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id UUID NOT NULL,
    consumer_type TEXT NOT NULL CHECK (consumer_type IN ('plugin_installation', 'agent', 'native')),
    consumer_id   UUID NOT NULL,
    enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    created_by    UUID,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (connection_id, consumer_type, consumer_id)
);

CREATE INDEX connection_binding_connection_idx ON connection_binding (connection_id);
CREATE INDEX connection_binding_consumer_idx ON connection_binding (consumer_type, consumer_id);
