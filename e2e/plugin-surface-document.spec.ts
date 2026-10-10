import { expect, test } from "@playwright/test";
import { buildSurfaceFrameDocument } from "../packages/views/plugins/surface-document";

// Hosted guest documents replaced inline code generation. Exercise lifecycle
// signals through the actual trusted wrapper in Chromium, with the host listener
// installed before mounting the frame (the same ordering as PluginSurface).
test.describe("plugin hosted surface document lifecycle", () => {
  test("a host-authored wrapper reload is not reported as hostile navigation", async ({ page }) => {
    await page.route("https://plugin-content.example.test/**", (route) => route.fulfill({
      contentType: "text/html",
      body: `<!doctype html><script>
        const channel = new MessageChannel();
        parent.postMessage({ type: "multica:plugin-bridge-connect", version: 2, challenge: "proof" }, "*", [channel.port1]);
      </script>`,
    }));
    const wrapper = buildSurfaceFrameDocument({
      url: "https://plugin-content.example.test/plugin-surfaces/opaque",
      bridgeToken: "proof",
    });
    await page.setContent(`<script>
      window.surfaceState = { connected: 0, navigated: 0 };
      addEventListener("message", event => {
        if (event.data?.type === "multica:plugin-bridge-connect") window.surfaceState.connected++;
        if (event.data?.type === "multica:plugin-surface-navigated") window.surfaceState.navigated++;
      });
    </script><iframe id="surface" sandbox="allow-scripts allow-same-origin"></iframe>`);
    const mount = () => page.locator("#surface").evaluate((frame, srcdoc) => {
      (frame as HTMLIFrameElement).srcdoc = srcdoc;
    }, wrapper);
    const connected = () => page.evaluate(() =>
      (window as unknown as { surfaceState: { connected: number } }).surfaceState.connected);
    await mount();
    await expect.poll(connected).toBe(1);
    await mount();
    await expect.poll(connected).toBe(2);
    expect(await page.evaluate(() =>
      (window as unknown as { surfaceState: { navigated: number } }).surfaceState.navigated)).toBe(0);
  });

  test("a guest bootstrap error terminates the wrapper before bridge connection", async ({ page }) => {
    await page.route("https://plugin-content.example.test/**", (route) => route.fulfill({
      contentType: "text/html",
      body: `<!doctype html><script>
        // The hosted bootstrap reports execution failures to its trusted wrapper.
        parent.postMessage({ type: "multica:plugin-surface-error" }, "*");
        parent.postMessage({ type: "multica:plugin-surface-error" }, "*");
        const channel = new MessageChannel();
        parent.postMessage({ type: "multica:plugin-bridge-connect", version: 2, challenge: "proof" }, "*", [channel.port1]);
      </script>`,
    }));
    await page.setContent(`<script>
      window.surfaceState = { errors: 0, connected: 0 };
      addEventListener("message", event => {
        if (event.source !== document.querySelector("#surface").contentWindow) return;
        if (event.data?.type === "multica:plugin-surface-error") window.surfaceState.errors++;
        if (event.data?.type === "multica:plugin-bridge-connect") window.surfaceState.connected++;
      });
    </script><iframe id="surface" sandbox="allow-scripts allow-same-origin"></iframe>`);
    await page.locator("#surface").evaluate((frame, srcdoc) => {
      (frame as HTMLIFrameElement).srcdoc = srcdoc;
    }, buildSurfaceFrameDocument({
      url: "https://plugin-content.example.test/plugin-surfaces/opaque",
      bridgeToken: "proof",
    }));
    await expect.poll(() => page.evaluate(() =>
      (window as unknown as { surfaceState: { errors: number; connected: number } }).surfaceState
    )).toEqual({ errors: 1, connected: 0 });
  });
});
