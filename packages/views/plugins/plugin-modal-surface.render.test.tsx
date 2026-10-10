import type { ReactNode } from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { expect, it, vi } from "vitest";
import { pluginKeys } from "@multica/core/plugins/queries";
import { featureFlagKeys } from "@multica/core/feature-flags";
import type { PluginInstallation, PluginSurface } from "@multica/core/types";
import { PluginModalSurface } from "./plugin-modal-surface";

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "workspace-1" }),
}));
vi.mock("@multica/core/config", () => ({ useFeatureEnabled: () => true }));
vi.mock("./plugin-surface-frame", () => ({
  PluginSurfaceFrame: ({ installation, surface }: {
    installation: PluginInstallation;
    surface: PluginSurface;
  }) => (
    <div data-testid="plugin-frame" data-version={installation.package_version_id}>
      {installation.name} — {surface.name}
    </div>
  ),
}));

function fixture(): PluginInstallation {
  return {
    id: "installation-1",
    plugin_key: "com.example.modal",
    name: "Example plugin",
    version: "1.0.0",
    package_version_id: "version-1",
    enabled: true,
    granted_scopes: [],
    config_schema: [],
    config: {},
    configured_secrets: [],
    surfaces: [{
      key: "dialog",
      type: "modal",
      name: "Dialog",
      entry: "ui.js",
      requires_flag: "host:composio_mcp_apps",
    }],
    hooks: [],
    resources: [],
    created_at: "",
    updated_at: "",
  };
}

function openModal(installation: PluginInstallation) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  client.setQueryData(pluginKeys.installed("workspace-1"), { plugins: [installation] });
  client.setQueryData(featureFlagKeys.all("workspace-1"), {
    definitions: [],
    resolved: {
      composio_mcp_apps: { value: true, source: "workspace", locked: false },
    },
    overrides: { workspace: {}, member: {} },
  });
  const target = { installation, surface: installation.surfaces[0]! };
  function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  }
  render(<PluginModalSurface target={target} onOpenChange={() => {}} />, { wrapper: Wrapper });
  expect(screen.getByTestId("plugin-frame")).toBeInTheDocument();
  return client;
}

it.each(["disabled", "uninstalled", "surface removed", "surface moved", "gate changed"])(
  "unmounts an already open host-gated modal when its current installation is %s",
  async (change) => {
    const initial = fixture();
    const client = openModal(initial);
    const current = {
      ...initial,
      enabled: change !== "disabled",
      surfaces: change === "surface removed" ? [] : initial.surfaces.map(
        (surface) => ({
          ...surface,
          type: change === "surface moved" ? "issue_panel" : surface.type,
          requires_flag: change === "gate changed" ? "new-gate" : surface.requires_flag,
        }),
      ),
    };
    act(() => client.setQueryData(pluginKeys.installed("workspace-1"), {
      plugins: change === "uninstalled" ? [] : [current],
    }));
    await waitFor(() => expect(screen.queryByTestId("plugin-frame")).not.toBeInTheDocument());
  },
);

it("renders current installation and surface metadata after an upgrade", async () => {
  const initial = fixture();
  const client = openModal(initial);
  act(() => client.setQueryData(pluginKeys.installed("workspace-1"), {
    plugins: [{
      ...initial,
      name: "Updated plugin",
      package_version_id: "version-2",
      surfaces: [{ ...initial.surfaces[0], name: "Updated dialog" }],
    }],
  }));
  await waitFor(() => expect(screen.getByTestId("plugin-frame")).toHaveAttribute("data-version", "version-2"));
  expect(screen.getByTestId("plugin-frame")).toHaveTextContent("Updated plugin — Updated dialog");
});
