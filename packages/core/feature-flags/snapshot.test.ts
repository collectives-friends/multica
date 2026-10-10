// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { ApiClient } from "../api/client";
import { featureFlagState, contributionEnabled } from "./state";
import { EMPTY_FEATURE_FLAGS } from "./schema";

describe("workspace flags", () => {
  it("never uses deployment defaults while waiting for a workspace", () => {
    expect(
      featureFlagState(undefined, "plugins_v1", true, true, false),
    ).toEqual({
      enabled: false,
      source: "pending",
      locked: false,
      settled: false,
    });
    expect(
      featureFlagState(undefined, "plugins_v1", true, false, false).enabled,
    ).toBe(true);
  });
  it("preserves resolved provenance and closes unknown contributions", () => {
    const snapshot = {
      ...EMPTY_FEATURE_FLAGS,
      resolved: {
        beta: { value: false, source: "parent:plugins_v1", locked: true },
      },
    };
    expect(featureFlagState(snapshot, "beta", true, true, true)).toEqual({
      enabled: false,
      source: "parent:plugins_v1",
      locked: true,
      settled: true,
    });
    expect(contributionEnabled("beta", "installation", snapshot.resolved)).toBe(
      false,
    );
    expect(contributionEnabled(undefined, "installation", {})).toBe(true);
  });
  it("parses malformed responses with a safe fallback", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          new Response(
            JSON.stringify({ resolved: { plugins_v1: { value: "yes" } } }),
            { status: 200 },
          ),
        ),
    );
    expect(await new ApiClient("http://test").getFeatureFlags("ws")).toEqual(
      EMPTY_FEATURE_FLAGS,
    );
    vi.unstubAllGlobals();
  });
});

it("encodes scoped mutation paths and reset methods without sending a value", async () => {
  const fetch = vi
    .fn()
    .mockImplementation(async () => new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetch);
  const client = new ApiClient("http://test");
  await client.setFeatureFlag("ws", {
    key: "plugin:com.example:beta",
    scope: "workspace",
    enabled: false,
    locked: true,
  });
  expect(fetch.mock.calls[0]?.[0]).toBe(
    "http://test/api/workspaces/ws/feature-flags/plugin%3Acom.example%3Abeta/workspace",
  );
  expect(JSON.parse(fetch.mock.calls[0]?.[1].body)).toEqual({
    enabled: false,
    locked: true,
  });
  await client.setFeatureFlag("ws", {
    key: "beta",
    scope: "member",
    enabled: null,
  });
  expect(fetch.mock.calls[1]?.[1]).toMatchObject({ method: "DELETE" });
  expect(fetch.mock.calls[1]?.[1].body).toBeUndefined();
  await client.setFeatureFlag("ws", {
    key: "beta",
    scope: "workspace",
    enabled: null,
  });
  expect(fetch.mock.calls[2]?.[0]).toBe(
    "http://test/api/workspaces/ws/feature-flags/beta/workspace",
  );
  expect(fetch.mock.calls[2]?.[1]).toMatchObject({ method: "DELETE" });
  vi.unstubAllGlobals();
});

it("sends the host-bound surface header on the existing action endpoint", async () => {
  const fetch = vi.fn().mockResolvedValue(new Response("{}", { status: 200 }));
  vi.stubGlobal("fetch", fetch);
  await new ApiClient("http://test").callPluginAction("installation", {
    method: "GET",
    path: "/context",
    surfaceKey: "own-panel",
  });
  expect(fetch.mock.calls[0]?.[0]).toBe(
    "http://test/api/plugin-bridge/v1/context",
  );
  expect(fetch.mock.calls[0]?.[1].headers).toMatchObject({
    "X-Multica-Plugin-Surface": "own-panel",
    "X-Multica-Plugin-Installation": "installation",
  });
  vi.unstubAllGlobals();
});
