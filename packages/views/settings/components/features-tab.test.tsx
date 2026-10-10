import type { ReactNode } from "react";
import { beforeEach, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import enSettings from "../../locales/en/settings.json";
import type { FeatureFlagDefinition } from "@multica/core/feature-flags";
const state = vi.hoisted(() => ({
  role: "admin",
  locked: false,
  value: true,
  source: undefined as string | undefined,
  frozen: undefined as boolean | undefined,
  extraDefinitions: [] as FeatureFlagDefinition[],
  override: undefined as { enabled: boolean; locked: boolean } | undefined,
  member: true,
}));
beforeEach(() => {
  state.role = "admin";
  state.locked = false;
  state.value = true;
  state.source = undefined;
  state.frozen = undefined;
  state.extraDefinitions = [];
  state.override = undefined;
  state.member = true;
  setFlag.mockClear();
});
const setFlag = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));
vi.mock("@multica/core/api", () => ({
  api: {
    getFeatureFlags: async () => ({
      definitions: [
        {
          key: "beta",
          label: "Beta panel",
          group: "Experiments",
          default: false,
          scope_policy: "member_overridable",
          public: true,
          frozen: state.frozen,
        },
        ...state.extraDefinitions,
      ],
      resolved: {
        beta: {
          value: state.value,
          source: state.source ?? (state.locked ? "workspace_locked" : "default"),
          locked: state.locked,
        },
      },
      overrides: {
        workspace: state.override ? { beta: state.override } : {},
        member: { beta: state.member },
      },
    }),
    setFeatureFlag: setFlag,
    getFeatureFlagAudit: async () => ({ entries: [] }),
  },
}));
vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "ws", name: "Test" }),
}));
vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ role: state.role }),
}));
import { FeaturesTab } from "./features-tab";
function Wrapper({ children }: { children: ReactNode }) {
  return (
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <I18nProvider locale="en" resources={{ en: { settings: enSettings } }}>
        {children}
      </I18nProvider>
    </QueryClientProvider>
  );
}
it("offers personal reset and exactly five workspace choices to admins", async () => {
  state.role = "admin";
  state.locked = false;
  setFlag.mockClear();
  render(<FeaturesTab />, { wrapper: Wrapper });
  await screen.findByText("Beta panel");
  await userEvent.click(
    screen.getByRole("button", { name: "Reset my choice" }),
  );
  await waitFor(() =>
    expect(setFlag).toHaveBeenCalledWith("ws", {
      key: "beta",
      scope: "member",
      enabled: null,
    }),
  );
  await userEvent.click(screen.getByRole("combobox", { name: "For whom" }));
  expect(await screen.findAllByRole("option")).toHaveLength(5);
  await userEvent.click(
    await screen.findByRole("option", { name: "Off for everyone (locked)" }),
  );
  await waitFor(() =>
    expect(setFlag).toHaveBeenCalledWith("ws", {
      key: "beta",
      scope: "workspace",
      enabled: false,
      locked: true,
    }),
  );
});
it("shows members a disabled personal switch when locked, without admin controls", async () => {
  state.role = "member";
  state.locked = true;
  render(<FeaturesTab />, { wrapper: Wrapper });
  await screen.findByText("Beta panel");
  expect(screen.getByRole("switch", { name: "My choice" })).toHaveAttribute(
    "aria-disabled",
    "true",
  );
  expect(
    screen.queryByRole("combobox", { name: "For whom" }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "History" }),
  ).not.toBeInTheDocument();
});

it.each([
  ["Use default", null, undefined],
  ["On by default (members can choose)", true, false],
  ["Off by default (members can choose)", false, false],
  ["On for everyone (locked)", true, true],
  ["Off for everyone (locked)", false, true],
] as const)(
  "writes the exact workspace choice: %s",
  async (label, enabled, locked) => {
    state.role = "admin";
    state.locked = false;
    state.override =
      enabled === null ? { enabled: true, locked: true } : undefined;
    setFlag.mockClear();
    render(<FeaturesTab />, { wrapper: Wrapper });
    await screen.findByText("Beta panel");
    await userEvent.click(screen.getByRole("combobox", { name: "For whom" }));
    await userEvent.click(await screen.findByRole("option", { name: label }));
    await waitFor(() =>
      expect(setFlag).toHaveBeenCalledWith("ws", {
        key: "beta",
        scope: "workspace",
        enabled,
        ...(enabled === null ? {} : { locked }),
      }),
    );
  },
);

it.each([
  [true, false],
  [false, true],
])("toggles a personal flag stored as %s to %s", async (stored, next) => {
  state.role = "member";
  state.member = stored;
  state.value = stored;
  state.source = "member";
  render(<FeaturesTab />, { wrapper: Wrapper });
  await screen.findByText("Beta panel");
  const toggle = screen.getByRole("switch", { name: "My choice" });
  expect(toggle).toHaveAttribute("aria-checked", String(stored));
  await userEvent.click(toggle);
  await waitFor(() =>
    expect(setFlag).toHaveBeenCalledWith("ws", {
      key: "beta",
      scope: "member",
      enabled: next,
    }),
  );
});

it("writes a member's personal choice without a workspace lock", async () => {
  state.role = "member";
  render(<FeaturesTab />, { wrapper: Wrapper });
  await screen.findByText("Beta panel");
  await userEvent.click(screen.getByRole("switch", { name: "My choice" }));
  await waitFor(() =>
    expect(setFlag).toHaveBeenCalledWith("ws", {
      key: "beta",
      scope: "member",
      enabled: false,
    }),
  );
});

it.each([
  ["workspace_locked", true, true, "Forced on"],
  ["workspace_locked", false, true, "Forced off"],
  ["deployment", false, true, "Forced off"],
  ["workspace", true, false, "Workspace default: On"],
  ["workspace", false, false, "Workspace default: Off"],
  ["parent:plugins_v1", false, true, "Off (parent)"],
] as const)(
  "shows effective state provenance for %s=%s",
  async (source, value, locked, label) => {
    state.source = source;
    state.value = value;
    state.locked = locked;
    render(<FeaturesTab />, { wrapper: Wrapper });
    const heading = await screen.findByText("Beta panel");
    expect(within(heading.closest("article")!).getByText(label)).toBeInTheDocument();
  },
);

it("keeps a flag frozen off immutable without relying on its resolved source", async () => {
  state.frozen = false;
  state.value = false;
  render(<FeaturesTab />, { wrapper: Wrapper });
  await screen.findByText("Beta panel");
  expect(screen.getByRole("combobox", { name: "For whom" })).toBeDisabled();
  expect(screen.getByRole("switch", { name: "My choice" })).toHaveAttribute(
    "aria-disabled",
    "true",
  );
  expect(screen.getByText("Forced off")).toBeInTheDocument();
  expect(
    screen.queryByText("This compatibility feature is always enabled."),
  ).not.toBeInTheDocument();
});

it("places every host group before plugin groups even when definitions arrive interleaved", async () => {
  state.extraDefinitions = [
    {
      key: "plugin:example:beta",
      label: "Plugin beta",
      description: "",
      group: "Plugins",
      default: false,
      public: true,
      scope_policy: "member_overridable",
      plugin_installation_id: "installation",
      plugin_name: "Example plugin",
    },
    {
      key: "search",
      label: "Local index",
      description: "",
      group: "Search",
      default: true,
      public: false,
      scope_policy: "workspace_only",
    },
  ];
  render(<FeaturesTab />, { wrapper: Wrapper });
  await screen.findByText("Local index");
  const headings = screen.getAllByRole("heading").map((heading) => heading.textContent);
  expect(headings.indexOf("Search")).toBeLessThan(headings.indexOf("Example plugin"));
});
