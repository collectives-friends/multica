import { parseWithFallback } from "../api/schema";
import { z } from "zod";
export const FeatureFlagDefinitionSchema = z.object({
  key: z.string(),
  label: z.string().default(""),
  description: z.string().default(""),
  group: z.string().default("Other"),
  default: z.boolean().default(false),
  scope_policy: z.string().default("workspace_only"),
  parent: z.string().optional(),
  public: z.boolean().default(false),
  frozen: z.boolean().default(false),
  plugin_installation_id: z.string().optional(),
  plugin_name: z.string().optional(),
});
export const FeatureFlagResolvedSchema = z.object({
  value: z.boolean(),
  source: z.string().default("default"),
  locked: z.boolean().default(false),
});
export const FeatureFlagsSchema = z.object({
  definitions: z.array(FeatureFlagDefinitionSchema).default([]),
  resolved: z.record(z.string(), FeatureFlagResolvedSchema).default({}),
  overrides: z
    .object({
      workspace: z
        .record(
          z.string(),
          z.object({
            enabled: z.boolean(),
            locked: z.boolean().default(false),
          }),
        )
        .default({}),
      member: z.record(z.string(), z.boolean()).default({}),
    })
    .default({ workspace: {}, member: {} }),
});
export const FeatureFlagAuditSchema = z.object({
  entries: z
    .array(
      z.object({
        id: z.string(),
        flag_key: z.string(),
        scope_type: z.string().default(""),
        actor_type: z.string().default(""),
        actor_id: z.string().nullable().default(null),
        action: z.string().default(""),
        reason: z.string().nullable().default(null),
        created_at: z.string(),
        old_enabled: z.boolean().nullable().optional(),
        new_enabled: z.boolean().nullable().optional(),
        old_locked: z.boolean().nullable().optional(),
        new_locked: z.boolean().nullable().optional(),
      }),
    )
    .default([]),
  next_before: z.string().optional(),
});
export type FeatureFlagsSnapshot = z.infer<typeof FeatureFlagsSchema>;
export type FeatureFlagDefinition = z.infer<typeof FeatureFlagDefinitionSchema>;
export type FeatureFlagAudit = z.infer<typeof FeatureFlagAuditSchema>;
export const EMPTY_FEATURE_FLAGS: FeatureFlagsSnapshot = {
  definitions: [],
  resolved: {},
  overrides: { workspace: {}, member: {} },
};

export function parsePluginContextFlags(raw: unknown) {
  return parseWithFallback(
    raw,
    z.object({ flags: z.record(z.string(), z.boolean()).default({}) }),
    { flags: {} as Record<string, boolean> },
    { endpoint: "plugin /context" },
  );
}
