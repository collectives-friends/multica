import type { FeatureFlagsSnapshot } from "./schema";
export function featureFlagState(
  snapshot: FeatureFlagsSnapshot | undefined,
  key: string,
  fallback: boolean,
  authenticated: boolean,
  settled: boolean,
) {
  if (!authenticated)
    return {
      enabled: fallback,
      source: "deployment",
      locked: false,
      settled: true,
    };
  const flag = snapshot?.resolved[key];
  return {
    enabled: flag?.value ?? false,
    source: flag?.source ?? (settled ? "unknown" : "pending"),
    locked: flag?.locked ?? false,
    settled,
  };
}
/** Server resolution remains authoritative; missing gated values fail closed. */
export function contributionEnabled(
  required: string | undefined,
  pluginKey: string,
  resolved: FeatureFlagsSnapshot["resolved"],
): boolean {
  if (!required) return true;
  const key = required.startsWith("host:")
    ? required.slice(5)
    : `plugin:${pluginKey}:${required}`;
  return resolved[key]?.value === true;
}
