// @vitest-environment node
import { afterEach, expect, it, vi } from "vitest";

const ports: MessagePort[] = [];
afterEach(() => { ports.splice(0).forEach((port) => port.close()); vi.unstubAllGlobals(); });

/** A host that answers every call except a hook the test wants left unanswered. */
async function sdk(answerHooks: boolean) {
  vi.resetModules();
  const channel = new MessageChannel();
  ports.push(channel.port1, channel.port2);
  vi.stubGlobal("__multicaPluginBridgePortV2", channel.port1);
  const seen: Array<Record<string, unknown>> = [];
  channel.port2.onmessage = ({ data }) => {
    const request = data as Record<string, unknown>;
    seen.push(request);
    if (!answerHooks && String(request.path).startsWith("/hooks/")) return;
    channel.port2.postMessage({ id: request.id, ok: true, status: 200, data: {} });
  };
  const multica = (await import("./index")).multica;
  return { multica, seen };
}

it("lets a caller shorten the watchdog without changing the call", async () => {
  const { multica, seen } = await sdk(false);
  const started = Date.now();
  await expect(
    multica.hooks.invoke("attio-action", { action: "publish" }, { timeoutMs: 40 }),
  ).rejects.toMatchObject({ status: 408 });
  // Rejected by the caller's own watchdog, not by the 15s default.
  expect(Date.now() - started).toBeLessThan(5000);
  expect(seen[seen.length - 1]).toMatchObject({ kind: "action", method: "POST", path: "/hooks/attio-action" });
});

it("answers a long hook normally when the host replies inside the extended window", async () => {
  const { multica, seen } = await sdk(true);
  await expect(multica.hooks.invoke("attio-action", { action: "preview" }, { timeoutMs: 5000 })).resolves.toEqual({});
  expect(seen[seen.length - 1]).toMatchObject({
    kind: "action",
    method: "POST",
    path: "/hooks/attio-action",
    body: { trigger: "ui", input: { action: "preview" } },
  });
});
