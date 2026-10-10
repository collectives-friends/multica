import type { QueryClient } from "@tanstack/react-query";
import type { StoreApi } from "zustand/vanilla";
/** Synchronous subscription prevents a new account's first render reading the previous snapshot. */
export function bindFeatureFlagSession<
  T extends { user: { id: string } | null },
>(qc: QueryClient, store: Pick<StoreApi<T>, "subscribe">) {
  return store.subscribe((next, previous) => {
    if (next.user?.id === previous.user?.id) return;
    qc.removeQueries({
      predicate: ({ queryKey }) =>
        queryKey[0] === "feature-flags" ||
        (queryKey[0] === "workspaces" && queryKey[2] === "plugins"),
    });
  });
}
