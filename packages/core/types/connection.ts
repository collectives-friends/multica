/**
 * A connection is one stored credential plus its non-secret configuration for
 * an external system. Plugins, agents and native modules are granted one
 * through a binding instead of each holding their own copy of the secret.
 *
 * These types mirror the server's non-secret response shape. A secret value is
 * never part of any response; `configured_secrets` carries field names only.
 */

export type ConnectionLevel = "instance" | "workspace";

export type ConnectionSecretMode = "host" | "reference";

export type ConnectionStatus =
  | "unchecked"
  | "ok"
  | "degraded"
  | "error"
  | "needs_reauth";

export type ConnectionConsumerType =
  | "plugin_installation"
  | "agent"
  | "native";

/** One host-rendered configuration input, same shape a plugin manifest uses. */
export interface ConnectionConfigField {
  key: string;
  type: "string" | "number" | "bool" | "enum" | "secret" | string;
  label: string;
  description?: string;
  required?: boolean;
  options?: string[];
  placeholder?: string;
  multiline?: boolean;
}

/** A code-defined connector description. Contains no secret. */
export interface ConnectionProvider {
  key: string;
  display_name: string;
  category: string;
  auth_kind: string;
  /** Secret field names the provider accepts, typically ["api_key"]. */
  secret_keys: string[];
  config_schema: ConnectionConfigField[];
  /** Exact hosts the provider reaches; the operator-facing network boundary. */
  net_domains: string[];
  capabilities: string[];
  /** Whether "Test connection" can actually probe this provider. */
  has_health: boolean;
}

export interface Connection {
  id: string;
  level: ConnectionLevel;
  workspace_id?: string;
  provider_key: string;
  name: string;
  environment: string;
  /** Non-secret configuration only. */
  config: Record<string, unknown>;
  secret_mode: ConnectionSecretMode;
  /** Names of stored secrets — never their values. */
  configured_secrets: string[];
  status: ConnectionStatus | string;
  status_detail: string;
  last_checked_at?: string;
  enabled: boolean;
  /** Increments on every accepted mutation; send it back on update. */
  revision: number;
  /** Instance connections, and every connection for a non-admin, are read-only. */
  read_only: boolean;
  created_at: string;
  updated_at: string;
}

export interface ConnectionListResponse {
  connections: Connection[];
  can_manage: boolean;
}

export interface ConnectionResponse {
  connection: Connection;
}

export interface ConnectionProviderListResponse {
  providers: ConnectionProvider[];
}

export interface ConnectionBinding {
  id: string;
  consumer_type: ConnectionConsumerType | string;
  consumer_id: string;
  enabled: boolean;
  created_at: string;
}

export interface ConnectionBindingListResponse {
  bindings: ConnectionBinding[];
}

export interface ConnectionBindingResponse {
  binding: ConnectionBinding;
}

export interface CreateConnectionRequest {
  provider_key: string;
  name: string;
  environment?: string;
  config?: Record<string, unknown>;
  secret_mode?: ConnectionSecretMode;
}

export interface UpdateConnectionRequest {
  name: string;
  environment?: string;
  config?: Record<string, unknown>;
  secret_mode?: ConnectionSecretMode;
  /** The revision the form was rendered from; a stale value is a 409. */
  expected_revision: number;
}

export interface SetConnectionSecretRequest {
  key: string;
  /** A value in host mode; an operator-owned pointer in reference mode. */
  value: string;
}

export interface BindConnectionRequest {
  consumer_type: ConnectionConsumerType;
  consumer_id: string;
}
