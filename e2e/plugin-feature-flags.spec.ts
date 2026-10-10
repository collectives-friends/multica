import { execFileSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { expect, test, type BrowserContext, type Page } from "@playwright/test";
import { TestApiClient } from "./fixtures";

const API_BASE = process.env.NEXT_PUBLIC_API_URL || `http://localhost:${process.env.PORT || "8080"}`;
const WEB_BASE = process.env.PLAYWRIGHT_BASE_URL ?? process.env.FRONTEND_ORIGIN ?? "http://localhost:3000";
const PLUGIN_KEY = "ai.multica.hello-panel";
const PANEL_FLAG = `plugin:${PLUGIN_KEY}:beta_panel`;
const HINT_FLAG = `plugin:${PLUGIN_KEY}:show_hint`;
const FRAME = 'iframe[title="Hello Panel — Hello"]';

type Workspace = { id: string; slug: string };
type Snapshot = {
  resolved: Record<string, { value: boolean; source: string; locked: boolean }>;
  overrides: { workspace: Record<string, unknown>; member: Record<string, boolean> };
};

async function apiRequest(api: TestApiClient, workspace: Workspace, path: string, init: RequestInit = {}) {
  const headers = new Headers(init.headers);
  headers.set("Authorization", `Bearer ${api.getToken()}`);
  headers.set("X-Workspace-ID", workspace.id);
  if (init.body && !(init.body instanceof FormData)) headers.set("Content-Type", "application/json");
  const response = await fetch(`${API_BASE}${path}`, { ...init, headers });
  expect(response.ok, `${init.method ?? "GET"} ${path}: ${response.status} ${response.statusText}`).toBe(true);
  return response;
}

async function enablePlugins(api: TestApiClient, workspace: Workspace) {
  await apiRequest(api, workspace, `/api/workspaces/${workspace.id}/feature-flags/plugins_v1/workspace`, {
    method: "PUT",
    body: JSON.stringify({ enabled: true, locked: false, reason: "E2E fixture" }),
  });
}

async function snapshot(api: TestApiClient, workspace: Workspace): Promise<Snapshot> {
  return (await apiRequest(api, workspace, `/api/workspaces/${workspace.id}/feature-flags`)).json();
}

async function publishAndInstall(api: TestApiClient, workspace: Workspace) {
  // Publish the real example, not a second manifest/script that can drift from it.
  const example = resolve("examples/plugins/hello-panel");
  const directory = await mkdtemp(join(tmpdir(), "multica-flags-e2e-"));
  try {
    const archive = join(directory, "hello-panel.zip");
    execFileSync("zip", ["-q", "-r", archive, "multica.plugin.json", "ui"], { cwd: example });
    const bundle = new FormData();
    bundle.set("bundle", new Blob([new Uint8Array(await readFile(archive))], { type: "application/zip" }), "hello-panel.zip");
    const published = await (await apiRequest(api, workspace, `/api/workspaces/${workspace.id}/plugins/packages`, { method: "POST", body: bundle })).json();
    const manifest = JSON.parse(await readFile(join(example, "multica.plugin.json"), "utf8"));
    expect(manifest.manifest_version).toBe(2);
    const installed = await (await apiRequest(api, workspace, `/api/workspaces/${workspace.id}/plugins`, {
      method: "POST",
      body: JSON.stringify({ version_id: published.versions[0].id, granted_scopes: manifest.scopes }),
    })).json();
    expect(installed.id).toEqual(expect.any(String));
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
}

async function authenticate(context: BrowserContext, api: TestApiClient) {
  const token = api.getToken();
  if (!token) throw new Error("Fixture login returned no token");
  await context.addInitScript((value) => {
    localStorage.setItem("multica_token", value);
    localStorage.setItem("multica:chat:isOpen", "false");
  }, token);
}

function panel(page: Page) {
  return page.frameLocator(FRAME).frameLocator("iframe");
}

async function chooseForWhom(page: Page, key: string, label: string) {
  // The anchor is on the article itself; keep the selector scoped to one flag.
  const feature = page.locator(`article[data-settings-anchor="${key}"]`);
  await feature.getByRole("combobox", { name: "For whom" }).click();
  await page.getByRole("option", { name: label, exact: true }).click();
}

test.describe("workspace feature flags and installed v2 plugin", () => {
  test("updates another member live, preserves workspace isolation, and supports personal reset and locking", async ({ browser }, testInfo) => {
    test.setTimeout(180_000);
    const run = randomUUID().slice(0, 8);
    const owner = new TestApiClient();
    const otherOwner = new TestApiClient();
    const member = new TestApiClient();
    let workspace: Workspace | undefined;
    let otherWorkspace: Workspace | undefined;
    const contexts: BrowserContext[] = [];
    try {
      await owner.login(`flags-owner-${run}@multica.ai`, "Flags Admin");
      workspace = await owner.ensureWorkspace(`Flags ${run}`, `flags-${run}`);
      await owner.markUserOnboarded();
      await enablePlugins(owner, workspace);

      await otherOwner.login(`flags-other-${run}@multica.ai`, "Other Admin");
      otherWorkspace = await otherOwner.ensureWorkspace(`Other flags ${run}`, `other-flags-${run}`);
      await otherOwner.markUserOnboarded();
      await enablePlugins(otherOwner, otherWorkspace);
      await member.login(`flags-member-${run}@multica.ai`, "Flags Member");
      await member.markUserOnboarded();
      await owner.addMemberByEmail(member.getEmail());
      await otherOwner.addMemberByEmail(member.getEmail());

      await publishAndInstall(owner, workspace);
      await publishAndInstall(otherOwner, otherWorkspace);
      const issue = await owner.createIssue(`Feature flag panel ${run}`);
      const otherIssue = await otherOwner.createIssue(`Other feature flag panel ${run}`);
      const ownerContext = await browser.newContext({ baseURL: WEB_BASE });
      contexts.push(ownerContext);
      const memberContext = await browser.newContext({ baseURL: WEB_BASE });
      contexts.push(memberContext);
      await authenticate(ownerContext, owner);
      await authenticate(memberContext, member);
      const ownerPage = await ownerContext.newPage();
      const memberPage = await memberContext.newPage();
      const otherPage = await memberContext.newPage();
      const memberSettings = await memberContext.newPage();
      await ownerPage.goto(`/${workspace.slug}/settings?tab=features`);
      await memberSettings.goto(`/${workspace.slug}/settings?tab=features`);
      await memberPage.goto(`/${workspace.slug}/issues/${issue.id}`);
      await otherPage.goto(`/${otherWorkspace.slug}/issues/${otherIssue.id}`);
      await expect(memberPage.locator(FRAME)).toBeVisible();
      await expect(otherPage.locator(FRAME)).toBeVisible();
      await expect(panel(memberPage).getByTestId("feature-hint")).toBeVisible();
      await expect(panel(memberPage).locator("#root")).toHaveAttribute("data-context-show-hint", "true");
      await memberPage.screenshot({ path: testInfo.outputPath("features-member-panel-on.png"), fullPage: true });
      const mainDocument = await memberPage.evaluate(() => performance.timeOrigin);
      const guestDocument = await panel(memberPage).locator("body").evaluate(() => performance.timeOrigin);
      const initialChanges = Number(await panel(memberPage).locator("#root").getAttribute("data-flag-changes"));
      const personalHint = memberSettings.locator(`article[data-settings-anchor="${HINT_FLAG}"]`);

      // A flag within an open surface proves delivery over the private bridge;
      // an unmount/remount alone cannot satisfy these assertions.
      await chooseForWhom(ownerPage, HINT_FLAG, "Off by default (members can choose)");
      await expect(panel(memberPage).getByTestId("feature-hint")).toBeHidden();
      await expect(panel(memberPage).locator("#root")).toHaveAttribute("data-context-show-hint", "false");
      await expect.poll(async () => Number(await panel(memberPage).locator("#root").getAttribute("data-flag-changes"))).toBeGreaterThan(initialChanges);
      expect(await panel(memberPage).locator("body").evaluate(() => performance.timeOrigin)).toBe(guestDocument);
      await expect(panel(otherPage).getByTestId("feature-hint")).toBeVisible();

      await personalHint.getByRole("switch", { name: "My choice" }).click();
      await expect(panel(memberPage).getByTestId("feature-hint")).toBeVisible();
      expect((await snapshot(member, workspace)).resolved[HINT_FLAG]).toMatchObject({ value: true, source: "member", locked: false });
      await personalHint.getByRole("button", { name: "Reset my choice" }).click();
      await expect(panel(memberPage).getByTestId("feature-hint")).toBeHidden();
      expect((await snapshot(member, workspace)).overrides.member).not.toHaveProperty(HINT_FLAG);

      await personalHint.getByRole("switch", { name: "My choice" }).click();
      await chooseForWhom(ownerPage, HINT_FLAG, "Off for everyone (locked)");
      await expect(panel(memberPage).getByTestId("feature-hint")).toBeHidden();
      expect((await snapshot(member, workspace)).resolved[HINT_FLAG]).toMatchObject({ value: false, source: "workspace_locked", locked: true });
      await expect(personalHint.getByRole("switch", { name: "My choice" })).toBeDisabled();
      await memberSettings.screenshot({ path: testInfo.outputPath("features-member.png"), fullPage: true });
      await ownerPage.screenshot({ path: testInfo.outputPath("features-admin.png"), fullPage: true });
      await memberPage.screenshot({ path: testInfo.outputPath("features-member-panel.png"), fullPage: true });

      await chooseForWhom(ownerPage, PANEL_FLAG, "Off for everyone (locked)");
      await expect(memberPage.locator(FRAME)).toHaveCount(0);
      await expect(otherPage.locator(FRAME)).toBeVisible();
      expect(await memberPage.evaluate(() => performance.timeOrigin)).toBe(mainDocument);
      expect((await snapshot(member, otherWorkspace)).resolved[PANEL_FLAG].value).toBe(true);
      await memberPage.screenshot({ path: testInfo.outputPath("features-member-panel-off.png"), fullPage: true });
      await writeFile(testInfo.outputPath("live-flags-evidence.json"), JSON.stringify({ workspaceIsolated: true, mainDocumentPreserved: true, guestDocumentPreservedForHintChange: true }, null, 2));
    } finally {
      await Promise.all(contexts.map((context) => context.close()));
      if (workspace) { await owner.cleanup(); await owner.deleteWorkspace(); }
      if (otherWorkspace) { await otherOwner.cleanup(); await otherOwner.deleteWorkspace(); }
    }
  });
  test("CLI list, get, set, unset and history use the live API", async () => {
    test.setTimeout(180_000);
    const directory = await mkdtemp(join(tmpdir(), "multica-flags-cli-e2e-"));
    const binary = join(directory, "multica");
    const owner = new TestApiClient();
    let workspace: Workspace | undefined;
    try {
      execFileSync("go", ["build", "-o", binary, "./cmd/multica"], { cwd: resolve("server"), stdio: "pipe" });
      const run = randomUUID().slice(0, 8);
      await owner.login(`flags-cli-${run}@multica.ai`, "Flags CLI Admin");
      workspace = await owner.ensureWorkspace(`CLI flags ${run}`, `cli-flags-${run}`);
      await enablePlugins(owner, workspace);
      await publishAndInstall(owner, workspace);
      // Keep credentials out of argv/output and isolate CLI profile lookup.
      const childEnv = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith("MULTICA_")));
      Object.assign(childEnv, {
        MULTICA_SERVER_URL: API_BASE,
        MULTICA_WORKSPACE_ID: workspace.id,
        MULTICA_TOKEN: owner.getToken()!,
        MULTICA_TASK_CONFIG_ROOT: directory,
      });
      const cli = (...args: string[]) => execFileSync(binary, ["feature", ...args], {
        cwd: directory, env: childEnv, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"], timeout: 30_000,
      });
      const json = (...args: string[]) => JSON.parse(cli(...args, "--output", "json"));
      expect(json("list", "--plugin", PLUGIN_KEY)).toEqual(expect.arrayContaining([
        expect.objectContaining({ key: HINT_FLAG, value: true }),
        expect.objectContaining({ key: PANEL_FLAG, value: true }),
      ]));
      expect(cli("list", "--plugin", PLUGIN_KEY, "--state", "on")).toContain(HINT_FLAG);
      expect(json("get", HINT_FLAG)).toMatchObject({ definition: { key: HINT_FLAG }, value: true, source: "default" });
      json("set", HINT_FLAG, "off", "--workspace", "--reason", "CLI E2E workspace override");
      expect((await snapshot(owner, workspace)).resolved[HINT_FLAG]).toMatchObject({ value: false, source: "workspace" });
      json("set", HINT_FLAG, "on");
      expect(json("get", HINT_FLAG)).toMatchObject({ value: true, source: "member" });
      json("set", HINT_FLAG, "off", "--workspace", "--locked");
      expect(json("get", HINT_FLAG)).toMatchObject({ value: false, source: "workspace_locked", locked: true });
      json("unset", HINT_FLAG, "--workspace");
      expect(json("get", HINT_FLAG)).toMatchObject({ value: true, source: "member" });
      json("unset", HINT_FLAG);
      expect(json("get", HINT_FLAG)).toMatchObject({ value: true, source: "default", overrides: {} });
      const history = json("history", HINT_FLAG);
      expect(history.current).toMatchObject({ value: true, source: "default" });
      expect(history.entries).toHaveLength(5);
      expect(history.entries).toEqual(expect.arrayContaining([
        expect.objectContaining({ flag_key: HINT_FLAG, scope_type: "workspace", action: "set", reason: "CLI E2E workspace override" }),
        expect.objectContaining({ flag_key: HINT_FLAG, scope_type: "member", action: "clear" }),
      ]));
    } finally {
      if (workspace) await owner.deleteWorkspace();
      await rm(directory, { recursive: true, force: true });
    }
  });

});
