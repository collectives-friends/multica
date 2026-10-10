CREATE TABLE workspace_feature_flag_override (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id UUID NOT NULL,
 scope_type TEXT NOT NULL CHECK (scope_type IN ('workspace','member')),
 scope_id UUID NOT NULL,
 flag_key TEXT NOT NULL CHECK (char_length(flag_key) BETWEEN 1 AND 255),
 enabled BOOLEAN NOT NULL,
 locked BOOLEAN NOT NULL DEFAULT FALSE,
 updated_by_type TEXT NOT NULL CHECK (updated_by_type IN ('member','agent','system')),
 updated_by_id UUID,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 CONSTRAINT wffo_locked_only_workspace CHECK (scope_type = 'workspace' OR locked = FALSE),
 CONSTRAINT wffo_workspace_scope CHECK (scope_type <> 'workspace' OR scope_id = workspace_id)
);
CREATE TABLE workspace_feature_flag_audit (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id UUID NOT NULL,
 flag_key TEXT NOT NULL CHECK (char_length(flag_key) BETWEEN 1 AND 255),
 scope_type TEXT NOT NULL CHECK (scope_type IN ('workspace','member')),
 scope_id UUID NOT NULL,
 action TEXT NOT NULL CHECK (action IN ('set','clear')),
 old_enabled BOOLEAN, old_locked BOOLEAN, new_enabled BOOLEAN, new_locked BOOLEAN,
 actor_type TEXT NOT NULL CHECK (actor_type IN ('member','agent','system')),
 actor_id UUID,
 reason TEXT CHECK (char_length(reason) <= 500),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
