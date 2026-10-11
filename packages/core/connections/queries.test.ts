import { describe, expect, it } from "vitest";
import { connectionKeys, connectionOptions, connectionsOptions } from "./queries";

describe("connection query keys", () => {
  it("scopes every key to the workspace so two workspaces never share a cache", () => {
    expect(connectionKeys.list("workspace-1")).not.toEqual(connectionKeys.list("workspace-2"));
    expect(connectionsOptions("workspace-1").queryKey).toEqual(connectionKeys.list("workspace-1"));
  });

  it("disables reads without a workspace or connection id", () => {
    expect(connectionsOptions("").enabled).toBe(false);
    expect(connectionOptions("workspace-1", "").enabled).toBe(false);
    expect(connectionOptions("workspace-1", "connection-1").enabled).toBe(true);
  });
});
