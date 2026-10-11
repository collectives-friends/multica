import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type {
  BindConnectionRequest,
  CreateConnectionRequest,
  SetConnectionSecretRequest,
  UpdateConnectionRequest,
} from "../types";
import { connectionKeys } from "./queries";

/**
 * Every write can change a connection's revision, status or bindings, so each
 * one invalidates the whole connections subtree rather than trying to patch
 * individual caches. The subtree is small; a wrong guess would be worse.
 */
function useInvalidateConnections(wsId: string) {
  const queryClient = useQueryClient();
  return () => queryClient.invalidateQueries({ queryKey: connectionKeys.all(wsId) });
}

export function useCreateConnection(wsId: string) {
  const invalidate = useInvalidateConnections(wsId);
  return useMutation({
    mutationFn: (request: CreateConnectionRequest) => api.createConnection(wsId, request),
    onSettled: invalidate,
  });
}

/** A 409 means the form was rendered from a stale revision; re-read and retry. */
export function useUpdateConnection(wsId: string) {
  const invalidate = useInvalidateConnections(wsId);
  return useMutation({
    mutationFn: ({ connectionId, request }: { connectionId: string; request: UpdateConnectionRequest }) =>
      api.updateConnection(wsId, connectionId, request),
    onSettled: invalidate,
  });
}

export function useDeleteConnection(wsId: string) {
  const invalidate = useInvalidateConnections(wsId);
  return useMutation({
    mutationFn: (connectionId: string) => api.deleteConnection(wsId, connectionId),
    onSettled: invalidate,
  });
}

export function useSetConnectionSecret(wsId: string) {
  const invalidate = useInvalidateConnections(wsId);
  return useMutation({
    mutationFn: ({ connectionId, request }: { connectionId: string; request: SetConnectionSecretRequest }) =>
      api.setConnectionSecret(wsId, connectionId, request),
    onSettled: invalidate,
  });
}

export function useDeleteConnectionSecret(wsId: string) {
  const invalidate = useInvalidateConnections(wsId);
  return useMutation({
    mutationFn: ({ connectionId, key }: { connectionId: string; key: string }) =>
      api.deleteConnectionSecret(wsId, connectionId, key),
    onSettled: invalidate,
  });
}

/** Runs the provider probe; the returned connection carries the new status. */
export function useTestConnection(wsId: string) {
  const invalidate = useInvalidateConnections(wsId);
  return useMutation({
    mutationFn: (connectionId: string) => api.testConnection(wsId, connectionId),
    onSettled: invalidate,
  });
}

export function useSetConnectionEnabled(wsId: string) {
  const invalidate = useInvalidateConnections(wsId);
  return useMutation({
    mutationFn: ({ connectionId, enabled }: { connectionId: string; enabled: boolean }) =>
      api.setConnectionEnabled(wsId, connectionId, enabled),
    onSettled: invalidate,
  });
}

export function useBindConnection(wsId: string) {
  const invalidate = useInvalidateConnections(wsId);
  return useMutation({
    mutationFn: ({ connectionId, request }: { connectionId: string; request: BindConnectionRequest }) =>
      api.bindConnection(wsId, connectionId, request),
    onSettled: invalidate,
  });
}

export function useUnbindConnection(wsId: string) {
  const invalidate = useInvalidateConnections(wsId);
  return useMutation({
    mutationFn: ({ connectionId, consumerType, consumerId }: { connectionId: string; consumerType: string; consumerId: string }) =>
      api.unbindConnection(wsId, connectionId, consumerType, consumerId),
    onSettled: invalidate,
  });
}
