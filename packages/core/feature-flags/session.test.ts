// @vitest-environment node
import { expect, it } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { createStore } from "zustand/vanilla";
import { bindFeatureFlagSession } from "./session";
it("clears flags and plugin projections on account switch and logout", () => {
  const qc = new QueryClient();
  const store = createStore(() => ({
    user: { id: "one" } as { id: string } | null,
  }));
  const unsubscribe = bindFeatureFlagSession(qc, store);
  const seed = () => {
    qc.setQueryData(["feature-flags", "ws"], { secret: true });
    qc.setQueryData(["workspaces", "ws", "plugins", "installed"], {
      plugins: [],
    });
  };
  seed();
  store.setState({ user: { id: "two" } });
  expect(qc.getQueryData(["feature-flags", "ws"])).toBeUndefined();
  expect(
    qc.getQueryData(["workspaces", "ws", "plugins", "installed"]),
  ).toBeUndefined();
  seed();
  store.setState({ user: null });
  expect(qc.getQueryData(["feature-flags", "ws"])).toBeUndefined();
  unsubscribe();
});
