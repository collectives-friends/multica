// @vitest-environment jsdom
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { configStore } from "../config";
import { EMPTY_FEATURE_FLAGS } from "./schema";
const state = vi.hoisted(() => ({
  status: "unauthenticated",
  slug: "a",
  user: { id: "one" },
}));
const getFlags = vi.hoisted(() => vi.fn());
vi.mock("../auth", () => ({
  useAuthStore: (
    selector: (s: { status: string; user: { id: string } }) => unknown,
  ) => selector(state),
}));
vi.mock("../paths/hooks", () => ({ useWorkspaceSlug: () => state.slug }));
vi.mock("../api", () => ({
  api: {
    getFeatureFlags: getFlags,
    listWorkspaces: async () => [
      { id: "a", slug: "a" },
      { id: "b", slug: "b" },
    ],
  },
}));
import { useFeatureFlag } from "./hooks";
let qc: QueryClient;
function Wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(
    ["workspaces", "list"],
    [
      { id: "a", slug: "a" },
      { id: "b", slug: "b" },
    ],
  );
  state.status = "unauthenticated";
  state.slug = "a";
  getFlags.mockReset();
  configStore.getState().setFeatureFlags({ plugins_v1: true });
});
it("uses config before login and waits for the selected workspace after login", async () => {
  let resolve!: (value: unknown) => void;
  getFlags.mockReturnValue(
    new Promise((r) => {
      resolve = r;
    }),
  );
  const hook = renderHook(() => useFeatureFlag("plugins_v1"), {
    wrapper: Wrapper,
  });
  expect(hook.result.current.enabled).toBe(true);
  expect(getFlags).not.toHaveBeenCalled();
  state.status = "authenticated";
  hook.rerender();
  expect(hook.result.current).toMatchObject({ enabled: false, settled: false });
  await act(async () =>
    resolve({
      ...EMPTY_FEATURE_FLAGS,
      resolved: {
        plugins_v1: { value: true, source: "workspace", locked: false },
      },
    }),
  );
  await waitFor(() =>
    expect(hook.result.current).toMatchObject({
      enabled: true,
      settled: true,
      source: "workspace",
    }),
  );
  state.slug = "b";
  getFlags.mockReturnValue(new Promise(() => {}));
  hook.rerender();
  expect(hook.result.current).toMatchObject({ enabled: false, settled: false });
});
it("fails closed on a scoped request failure", async () => {
  state.status = "authenticated";
  getFlags.mockRejectedValue(new Error("unavailable"));
  const hook = renderHook(() => useFeatureFlag("plugins_v1", true), {
    wrapper: Wrapper,
  });
  await waitFor(() => expect(hook.result.current.settled).toBe(true));
  expect(hook.result.current.enabled).toBe(false);
});

it("closes a previously enabled flag when its refresh fails", async () => {
  state.status = "authenticated";
  getFlags.mockResolvedValue({
    ...EMPTY_FEATURE_FLAGS,
    resolved: {
      plugins_v1: { value: true, source: "workspace", locked: false },
    },
  });
  const hook = renderHook(() => useFeatureFlag("plugins_v1"), {
    wrapper: Wrapper,
  });
  await waitFor(() => expect(hook.result.current.enabled).toBe(true));
  getFlags.mockRejectedValue(new Error("database unavailable"));
  await act(async () => {
    await qc.invalidateQueries({ queryKey: ["feature-flags", "a"] });
  });
  await waitFor(() => expect(hook.result.current.enabled).toBe(false));
});
