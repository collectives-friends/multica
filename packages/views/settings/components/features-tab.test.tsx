import type { ReactNode } from "react";
import { expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import enSettings from "../../locales/en/settings.json";
const state = vi.hoisted(() => ({
  role: "admin",
  locked: false,
  override: undefined as { enabled: boolean; locked: boolean } | undefined,
}));
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
          frozen: false,
        },
      ],
      resolved: {
        beta: {
          value: true,
          source: state.locked ? "workspace_locked" : "default",
          locked: state.locked,
        },
      },
      overrides: {
        workspace: state.override ? { beta: state.override } : {},
        member: { beta: true },
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
  expect(screen.getAllByRole("option")).toHaveLength(5);
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

it("writes a member's personal choice without a workspace lock",async()=>{
 state.role="member";state.locked=false;state.override=undefined;setFlag.mockClear();
 render(<FeaturesTab/>,{wrapper:Wrapper});await screen.findByText("Beta panel");
 await userEvent.click(screen.getByRole("switch",{name:"My choice"}));
 await waitFor(()=>expect(setFlag).toHaveBeenCalledWith("ws",{key:"beta",scope:"member",enabled:false}));
});
