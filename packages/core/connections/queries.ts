import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const connectionKeys = {
  all: (wsId: string) => ["workspaces", wsId, "connections"] as const,
  list: (wsId: string) => [...connectionKeys.all(wsId), "list"] as const,
  providers: (wsId: string) => [...connectionKeys.all(wsId), "providers"] as const,
  detail: (wsId: string, connectionId: string) =>
    [...connectionKeys.all(wsId), connectionId] as const,
  bindings: (wsId: string, connectionId: string) =>
    [...connectionKeys.all(wsId), connectionId, "bindings"] as const,
};

/**
 * The built-in provider registry. It is code-defined on the server, so it
 * changes only on deploy; a long stale time keeps opening the tab from
 * re-reading what cannot have changed.
 */
export function connectionProvidersOptions(wsId: string) {
  return queryOptions({
    queryKey: connectionKeys.providers(wsId),
    queryFn: () => api.listConnectionProviders(wsId),
    enabled: wsId.length > 0,
    staleTime: 5 * 60_000,
  });
}

/** The workspace's connections plus the deployment's instance connections. */
export function connectionsOptions(wsId: string) {
  return queryOptions({
    queryKey: connectionKeys.list(wsId),
    queryFn: () => api.listConnections(wsId),
    enabled: wsId.length > 0,
  });
}

export function connectionOptions(wsId: string, connectionId: string) {
  return queryOptions({
    queryKey: connectionKeys.detail(wsId, connectionId),
    queryFn: () => api.getConnection(wsId, connectionId),
    enabled: wsId.length > 0 && connectionId.length > 0,
  });
}

/** Who may currently use this connection. Names, never credentials. */
export function connectionBindingsOptions(wsId: string, connectionId: string) {
  return queryOptions({
    queryKey: connectionKeys.bindings(wsId, connectionId),
    queryFn: () => api.listConnectionBindings(wsId, connectionId),
    enabled: wsId.length > 0 && connectionId.length > 0,
  });
}
