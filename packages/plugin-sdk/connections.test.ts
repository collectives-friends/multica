// @vitest-environment node
import { afterEach, expect, it, vi } from "vitest";

const ports: MessagePort[] = [];
afterEach(() => { ports.splice(0).forEach((port) => port.close()); vi.unstubAllGlobals(); });

/** A host that records what the SDK sent and replies with `reply(data)`. */
async function sdk(reply: (request: Record<string, unknown>) => unknown) {
  vi.resetModules();
  const channel = new MessageChannel();
  ports.push(channel.port1, channel.port2);
  vi.stubGlobal("__multicaPluginBridgePortV2", channel.port1);
  const seen: Array<Record<string, unknown>> = [];
  channel.port2.onmessage = ({ data }) => {
    seen.push(data as Record<string, unknown>);
    channel.port2.postMessage({ id: data.id, ok: true, status: 200, data: reply(data) });
  };
  const multica = (await import("./index")).multica;
  return { multica, seen };
}

it("lists bound connections and defaults to an empty list", async () => {
  const { multica, seen } = await sdk(() => ({}));
  expect(await multica.connections.list()).toEqual([]);
  expect(seen[0]).toMatchObject({ kind: "action", method: "GET", path: "/connections" });
});

it("asks the host to perform a provider-relative call", async () => {
  const { multica, seen } = await sdk(() => ({ status: 201, content_type: "application/json", body: "{}" }));
  const response = await multica.connections.request("conn-1", {
    method: "POST",
    path: "/v2/records",
    query: { limit: "10" },
    body: { a: 1 },
  });
  expect(response.status).toBe(201);
  expect(seen[0]).toMatchObject({ method: "POST", path: "/connections/conn-1/request" });
  // The surface names no host: only the path the provider's base URL is joined
  // with, which is what keeps the credential's destination fixed.
  expect(seen[0]?.body).toEqual({ method: "POST", path: "/v2/records", query: { limit: "10" }, body: { a: 1 } });
});

it("encodes the connection id so a crafted id cannot change the path", async () => {
  const { multica, seen } = await sdk(() => ({}));
  await multica.connections.request("a/b", { method: "GET", path: "/v2/self" });
  expect(seen[0]?.path).toBe("/connections/a%2Fb/request");
});
