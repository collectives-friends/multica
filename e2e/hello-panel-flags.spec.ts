import { readFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";

const script = await readFile("examples/plugins/hello-panel/ui/main.js", "utf8");

// Exercises the actual single-file example without an API, database, or host
// app. The separate plugin-feature-flags scenario owns live cross-user wiring.
test.describe("Hello Panel flag subscription", () => {
  for (const earlyPush of [false, true]) {
    test(`updates the same document and cached context${earlyPush ? " when a push precedes initial context" : ""}`, async ({ page }) => {
      await page.setContent('<div id="root"></div>');
      await page.evaluate((pushBeforeContext) => {
        const channel = new MessageChannel();
        const state = window as unknown as {
          __multicaPluginBridgePortV2: MessagePort;
          __exampleHostPort: MessagePort;
          __contextReads: number;
        };
        state.__multicaPluginBridgePortV2 = channel.port2;
        state.__exampleHostPort = channel.port1;
        state.__contextReads = 0;
        channel.port1.onmessage = ({ data }) => {
          if (data.kind !== "action") return;
          let result: unknown = null;
          if (data.path === "/context") {
            state.__contextReads++;
            if (pushBeforeContext) channel.port1.postMessage({ kind: "flags", flags: { beta_panel: true, show_hint: false } });
            result = {
              user: { id: "member", name: "Member" },
              workspace: { id: "workspace", name: "Workspace" },
              issue: { id: "issue", identifier: "TEST-1" },
              flags: { beta_panel: true, show_hint: true },
            };
          } else if (data.path === "/issues/issue") result = { title: "Live flags example" };
          channel.port1.postMessage({ id: data.id, ok: true, data: result });
        };
        channel.port1.start();
      }, earlyPush);
      await page.addScriptTag({ content: script });
      await expect(page.locator("#title")).toHaveText("Live flags example");
      await expect(page.locator("#root")).toHaveAttribute("data-context-show-hint", String(!earlyPush));
      const documentIdentity = await page.evaluate(() => performance.timeOrigin);
      const initialChanges = Number(await page.locator("#root").getAttribute("data-flag-changes"));
      await page.evaluate(() => {
        (window as unknown as { __exampleHostPort: MessagePort }).__exampleHostPort.postMessage({ kind: "flags", flags: { beta_panel: true, show_hint: false } });
      });
      await expect(page.getByTestId("feature-hint")).toBeHidden();
      await expect(page.locator("#root")).toHaveAttribute("data-context-show-hint", "false");
      await expect.poll(async () => Number(await page.locator("#root").getAttribute("data-flag-changes"))).toBeGreaterThan(initialChanges);
      await page.evaluate(() => {
        (window as unknown as { __exampleHostPort: MessagePort }).__exampleHostPort.postMessage({ kind: "flags", flags: { beta_panel: true, show_hint: true } });
      });
      await expect(page.getByTestId("feature-hint")).toBeVisible();
      await expect(page.locator("#root")).toHaveAttribute("data-context-show-hint", "true");
      expect(await page.evaluate(() => performance.timeOrigin)).toBe(documentIdentity);
      expect(await page.evaluate(() => (window as unknown as { __contextReads: number }).__contextReads)).toBe(1);
    });
  }
});
