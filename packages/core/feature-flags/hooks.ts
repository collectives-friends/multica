"use client";
import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "../auth";
import { useWorkspaceSlug } from "../paths/hooks";
import { workspaceListOptions } from "../workspace/queries";
import { useConfigStore } from "../config";
import { featureFlagsOptions } from "./queries";
import { featureFlagState } from "./state";
export function useFeatureFlag(key: string, defaultValue = false) {
  const slug = useWorkspaceSlug();
  const status = useAuthStore((s) => s.status);
  const userId = useAuthStore((s) => s.user?.id);
  const authenticated = status !== "unauthenticated";
  const { data: workspaces } = useQuery({
    ...workspaceListOptions(),
    enabled: status === "authenticated" && !!userId && !!slug,
  });
  const workspace = workspaces?.find((workspace) => workspace.slug === slug);
  const fallback = useConfigStore((s) => s.featureFlags[key] ?? defaultValue);
  const query = useQuery({
    ...featureFlagsOptions(workspace?.id ?? ""),
    enabled: status === "authenticated" && !!userId && !!workspace?.id,
  });
  return featureFlagState(
    query.isError ? undefined : query.data,
    key,
    fallback,
    authenticated,
    !!workspace && !query.isPending,
  );
}
