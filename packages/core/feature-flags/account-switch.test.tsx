// @vitest-environment jsdom
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { create } from "zustand";
import { expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { registerAuthStore } from "../auth";
import type { AuthState } from "../auth/store";
import { WorkspaceSlugProvider } from "../paths/hooks";
import { EMPTY_FEATURE_FLAGS } from "./schema";
import { bindFeatureFlagSession } from "./session";
import { useFeatureFlag } from "./hooks";
const getFlags = vi.hoisted(() => vi.fn());
vi.mock("../api", () => ({
  api: {
    getFeatureFlags: getFlags,
    listWorkspaces: async () => [{ id: "a", slug: "a" }],
  },
}));
it("drops the first account's personal resolution before the second account resolves", async () => {
  const store = create<AuthState>(
    () => ({ user: { id: "first" }, status: "authenticated" }) as AuthState,
  );
  registerAuthStore(store);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(["workspaces", "list"], [{ id: "a", slug: "a" }]);
  const unbind = bindFeatureFlagSession(qc, store);
  getFlags.mockResolvedValue({
    ...EMPTY_FEATURE_FLAGS,
    resolved: { beta: { value: true, source: "member", locked: false } },
  });
  function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={qc}>
        <WorkspaceSlugProvider slug="a">{children}</WorkspaceSlugProvider>
      </QueryClientProvider>
    );
  }
  const hook = renderHook(() => useFeatureFlag("beta"), { wrapper: Wrapper });
  await waitFor(() => expect(hook.result.current.enabled).toBe(true));
  getFlags.mockReturnValue(new Promise(() => {}));
  act(() => store.setState({ user: { id: "second" } as AuthState["user"] }));
  expect(hook.result.current).toMatchObject({ enabled: false, settled: false });
  expect(getFlags).toHaveBeenCalledTimes(2);
  unbind();
  hook.unmount();
  qc.clear();
});
