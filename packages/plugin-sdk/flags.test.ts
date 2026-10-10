// @vitest-environment node
import { afterEach, expect, it, vi } from "vitest";
const ports: MessagePort[] = [];
afterEach(() => { ports.splice(0).forEach(port => port.close()); vi.unstubAllGlobals(); });
async function sdk(flags?: Record<string, boolean>) {
  vi.resetModules();
  const channel = new MessageChannel(); ports.push(channel.port1, channel.port2);
  vi.stubGlobal("__multicaPluginBridgePortV2", channel.port1);
  channel.port2.onmessage = ({ data }) => channel.port2.postMessage({ id: data.id, ok: true, status: 200, data: { workspace: {}, user: {}, config: {}, flags } });
  return { multica: (await import("./index")).multica, host: channel.port2 };
}
it("reads flags through context and tolerates older hosts", async () => {
  const { multica } = await sdk();
  expect(await multica.flags.get("missing")).toBe(false);
  expect(await multica.flags.get("missing", true)).toBe(true);
  expect(await multica.flags.all()).toEqual({});
});
it.each(["constructor", "__proto__", "toString"])(
  "uses the fallback for an absent flag named %s",
  async (key) => {
    const { multica } = await sdk();
    expect(await multica.flags.get(key)).toBe(false);
    expect(await multica.flags.get(key, true)).toBe(true);
  },
);
it.each([true, false])("reads an own constructor flag set to %s", async (value) => {
  const { multica } = await sdk({ constructor: value });
  expect(await multica.flags.get("constructor", !value)).toBe(value);
});
it("uses the fallback for a nonboolean context flag", async () => {
  const { multica } = await sdk(JSON.parse('{"beta":"enabled"}'));
  expect(await multica.flags.get("beta")).toBe(false);
  expect(await multica.flags.get("beta", true)).toBe(true);
});
it("retains an event before the first get and updates cached context", async () => {
  const { multica, host } = await sdk({ beta: false });
  const listener = vi.fn(); const unsubscribe = multica.flags.onChange(listener);
  host.postMessage({ kind: "flags", flags: { beta: true, "host:plugins_v1": true } });
  await vi.waitFor(() => expect(listener).toHaveBeenCalledWith({ beta: true, "host:plugins_v1": true }));
  expect(await multica.flags.get("beta")).toBe(true);
  expect((await multica.context.get()).flags?.beta).toBe(true);
  unsubscribe(); listener.mockClear();
  host.postMessage({ kind: "flags", flags: { beta: false } });
  await vi.waitFor(async () => expect(await multica.flags.get("beta")).toBe(false));
  expect(listener).not.toHaveBeenCalled();
  expect((await multica.context.get()).flags?.beta).toBe(false);
});
it("ignores unknown events and malformed flag values", async () => {
  const { multica, host } = await sdk({ beta: true });
  await multica.context.get();
  host.postMessage({ kind: "future", flags: { beta: false } });
  host.postMessage({ kind: "flags", flags: { beta: "false" } });
  expect(await multica.flags.get("beta")).toBe(true);
});

it("the shipped 0.1.0 SDK ignores the new event kind",async()=>{
  vi.resetModules(); const channel=new MessageChannel();ports.push(channel.port1,channel.port2);
  vi.stubGlobal("__multicaPluginBridgePortV2",channel.port1);
  const old=await import("./test-fixtures/v0.1.0/index");
  const theme=vi.fn();old.multica.ui.onThemeChange(theme);
  channel.port2.postMessage({kind:"flags",flags:{beta:true}});
  channel.port2.postMessage({kind:"theme",theme:{"--background":"black"}});
  await vi.waitFor(()=>expect(theme).toHaveBeenCalledOnce());
  expect(theme).toHaveBeenCalledWith({"--background":"black"});
});
