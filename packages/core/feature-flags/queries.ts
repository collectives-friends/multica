import {
  type QueryClient,
  type QueryFilters,
  queryOptions,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { api } from "../api";
import { pluginKeys } from "../plugins/queries";
export const featureFlagKeys = {
  all: (wsId: string) => ["feature-flags", wsId] as const,
};
export function featureFlagsOptions(wsId: string) {
  return queryOptions({
    queryKey: featureFlagKeys.all(wsId),
    queryFn: () => api.getFeatureFlags(wsId),
    enabled: !!wsId,
    staleTime: 30_000,
  });
}
export function featureFlagAuditOptions(
  wsId: string,
  key: string,
  before?: string,
) {
  return queryOptions({
    queryKey: [...featureFlagKeys.all(wsId), "audit", key, before],
    queryFn: () => api.getFeatureFlagAudit(wsId, key, before),
    enabled: !!wsId && !!key,
  });
}
export function useSetFeatureFlag(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      key: string;
      scope: "member" | "workspace";
      enabled: boolean | null;
      locked?: boolean;
      reason?: string;
    }) => api.setFeatureFlag(wsId, input),
    onSuccess: () => refreshFeatureFlags(qc, wsId),
  });
}

/** Cancel cold requests too, so an event cannot dedupe onto a pre-change snapshot. */
export async function refreshFeatureFlags(qc: QueryClient, wsId: string) {
  const filters: QueryFilters[] = [
    { queryKey: featureFlagKeys.all(wsId) },
    // Surface launches carry single-use tokens and must survive a flag push.
    { queryKey: pluginKeys.installed(wsId) },
  ];
  await Promise.all(filters.map(filter => qc.cancelQueries(filter)));
  await Promise.all(filters.map(filter => qc.invalidateQueries(filter)));
}
