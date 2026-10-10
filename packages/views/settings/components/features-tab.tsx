"use client";
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import {
  featureFlagsOptions,
  featureFlagAuditOptions,
  useSetFeatureFlag,
  type FeatureFlagDefinition,
  type FeatureFlagsSnapshot,
} from "@multica/core/feature-flags";
import { Button } from "@multica/ui/components/ui/button";
import { Badge } from "@multica/ui/components/ui/badge";
import { Input } from "@multica/ui/components/ui/input";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@multica/ui/components/ui/sheet";
import { useT } from "../../i18n";
import { SettingsSection } from "./settings-layout";

export function FeaturesTab() {
  const { t } = useT("settings");
  const wsId = useCurrentWorkspace()?.id ?? "";
  const { role } = useCurrentMember(wsId);
  const query = useQuery(featureFlagsOptions(wsId));
  const [search, setSearch] = useState("");
  const [group, setGroup] = useState("all");
  const [state, setState] = useState("all");
  const groups = [
    ...new Set(
      [...(query.data?.definitions ?? [])]
        .sort(
          (a, b) =>
            Number(!!a.plugin_installation_id) - Number(!!b.plugin_installation_id),
        )
        .map((d) =>
          d.plugin_installation_id
            ? `plugin:${d.plugin_installation_id}`
            : d.group,
        ),
    ),
  ];
  const groupLabel = (value: string) =>
    query.data?.definitions.find(
      (d) => `plugin:${d.plugin_installation_id}` === value,
    )?.plugin_name ?? value;
  const definitions = (query.data?.definitions ?? []).filter((d) => {
    const resolved = query.data?.resolved[d.key];
    return (
      `${d.label} ${d.key} ${d.description} ${d.plugin_name ?? ""}`
        .toLowerCase()
        .includes(search.toLowerCase()) &&
      (group === "all" ||
        group ===
          (d.plugin_installation_id
            ? `plugin:${d.plugin_installation_id}`
            : d.group)) &&
      (state === "all" ||
        (state === "on" && resolved?.value) ||
        (state === "off" && !resolved?.value) ||
        (state === "locked" && resolved?.locked))
    );
  });
  return (
    <div className="space-y-6">
      <h2 className="text-title-sm font-semibold">
        {t(($) => $.features.title)}
      </h2>
      <div className="flex flex-wrap gap-2" data-settings-anchor="features">
        <Input
          aria-label={t(($) => $.features.search)}
          placeholder={t(($) => $.features.search)}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="max-w-sm"
        />
        <FeatureSelect
          label={t(($) => $.features.group)}
          value={group}
          onChange={setGroup}
          items={[
            { value: "all", label: t(($) => $.features.all_groups) },
            ...groups.map((value) => ({ value, label: groupLabel(value) })),
          ]}
        />
        <FeatureSelect
          label={t(($) => $.features.state)}
          value={state}
          onChange={setState}
          items={[
            { value: "all", label: t(($) => $.features.all_states) },
            { value: "on", label: t(($) => $.features.on) },
            { value: "off", label: t(($) => $.features.off) },
            { value: "locked", label: t(($) => $.features.locked) },
          ]}
        />
      </div>
      {query.isPending ? (
        <p role="status">{t(($) => $.features.loading)}</p>
      ) : query.isError ? (
        <div role="alert">
          {t(($) => $.features.load_failed)}{" "}
          <Button variant="outline" onClick={() => void query.refetch()}>
            {t(($) => $.features.retry)}
          </Button>
        </div>
      ) : definitions.length === 0 ? (
        <p>{t(($) => $.features.empty)}</p>
      ) : (
        groups.map((value) => {
          const entries = definitions.filter(
            (d) =>
              (d.plugin_installation_id
                ? `plugin:${d.plugin_installation_id}`
                : d.group) === value,
          );
          if (!entries.length) return null;
          return (
            <SettingsSection key={value} title={groupLabel(value)}>
              <div className="space-y-3">
                {entries.map((definition) => (
                  <FeatureRow
                    key={definition.key}
                    wsId={wsId}
                    definition={definition}
                    snapshot={query.data!}
                    canManage={role === "admin" || role === "owner"}
                  />
                ))}
              </div>
            </SettingsSection>
          );
        })
      )}
    </div>
  );
}

function FeatureSelect({
  label,
  value,
  items,
  onChange,
  disabled = false,
}: {
  label: string;
  value: string;
  items: { value: string; label: string }[];
  onChange: (value: string) => void;
  disabled?: boolean;
}) {
  return (
    <Select
      value={value}
      items={items}
      onValueChange={(value) => {
        if (value !== null) onChange(value);
      }}
      disabled={disabled}
    >
      <SelectTrigger aria-label={label}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {items.map((item) => (
          <SelectItem key={item.value} value={item.value}>
            {item.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

function FeatureRow({
  wsId,
  definition: d,
  snapshot,
  canManage,
}: {
  wsId: string;
  definition: FeatureFlagDefinition;
  snapshot: FeatureFlagsSnapshot;
  canManage: boolean;
}) {
  const { t } = useT("settings");
  const mutation = useSetFeatureFlag(wsId);
  const resolved = snapshot.resolved[d.key];
  const override = snapshot.overrides.workspace[d.key];
  const source = resolved?.source ?? "default";
  const frozen = d.frozen !== undefined || source === "frozen";
  const immutable = frozen || source === "deployment";
  const parentOff = source.startsWith("parent:");
  const valueLabel = resolved?.value
    ? t(($) => $.features.on)
    : t(($) => $.features.off);
  const stateLabel = parentOff
    ? t(($) => $.features.off_parent)
    : immutable || resolved?.locked
      ? resolved?.value
        ? t(($) => $.features.forced_on)
        : t(($) => $.features.forced_off)
      : source === "workspace"
        ? t(($) => $.features.workspace_default, { state: valueLabel })
        : valueLabel;
  const personalDisabled =
    immutable || parentOff || resolved?.locked || mutation.isPending;
  const forWhom = !override
    ? "default"
    : `${override.locked ? "locked" : "unlocked"}_${override.enabled ? "on" : "off"}`;
  const reason =
    frozen
      ? t(($) => $.features.frozen)
      : source === "deployment"
        ? t(($) => $.features.deployment)
        : parentOff
          ? t(($) => $.features.parent_off, { parent: source.slice(7) })
          : resolved?.locked
            ? t(($) => $.features.workspace_locked)
            : source === "member"
              ? t(($) => $.features.personal_source)
              : source === "workspace"
                ? t(($) => $.features.workspace_source)
                : source === "rule"
                  ? t(($) => $.features.rule_source)
                  : t(($) => $.features.default_source);
  return (
    <article
      className="space-y-3 rounded-lg border border-surface-border p-4"
      data-settings-anchor={d.key}
    >
      <div className="flex flex-wrap justify-between gap-2">
        <div className="min-w-0">
          <h3 className="text-body font-medium">{d.label || d.key}</h3>
          {d.description && (
            <p className="text-caption text-muted-foreground">
              {d.description}
            </p>
          )}
          <p className="break-all text-caption text-muted-foreground">
            {d.key}
          </p>
        </div>
        <Badge variant="secondary" className="self-start">
          {stateLabel}
        </Badge>
      </div>
      <p className="text-caption text-muted-foreground">{reason}</p>
      <div className="flex flex-wrap items-center gap-3">
        {d.scope_policy === "member_overridable" && (
          <>
            <label className="flex items-center gap-2">
              <Switch
                checked={resolved?.value ?? false}
                disabled={personalDisabled}
                onCheckedChange={(enabled) =>
                  mutation.mutate({
                    key: d.key,
                    scope: "member",
                    enabled: enabled === true,
                  })
                }
              />
              {t(($) => $.features.my_choice)}
            </label>
            <Button
              variant="ghost"
              size="sm"
              disabled={
                personalDisabled ||
                snapshot.overrides.member[d.key] === undefined
              }
              onClick={() =>
                mutation.mutate({ key: d.key, scope: "member", enabled: null })
              }
            >
              {t(($) => $.features.reset)}
            </Button>
          </>
        )}
        {canManage && (
          <>
            <FeatureSelect
              label={t(($) => $.features.for_whom)}
              value={forWhom}
              disabled={immutable || mutation.isPending}
              items={[
                {
                  value: "default",
                  label: t(($) => $.features.choices.default),
                },
                {
                  value: "unlocked_on",
                  label:
                    d.scope_policy === "member_overridable"
                      ? t(($) => $.features.choices.unlocked_on)
                      : t(($) => $.features.workspace_on),
                },
                {
                  value: "unlocked_off",
                  label:
                    d.scope_policy === "member_overridable"
                      ? t(($) => $.features.choices.unlocked_off)
                      : t(($) => $.features.workspace_off),
                },
                {
                  value: "locked_on",
                  label: t(($) => $.features.choices.locked_on),
                },
                {
                  value: "locked_off",
                  label: t(($) => $.features.choices.locked_off),
                },
              ]}
              onChange={(value) =>
                mutation.mutate(
                  value === "default"
                    ? { key: d.key, scope: "workspace", enabled: null }
                    : {
                        key: d.key,
                        scope: "workspace",
                        enabled: value.endsWith("_on"),
                        locked: value.startsWith("locked_"),
                      },
                )
              }
            />
            <FlagHistory wsId={wsId} flagKey={d.key} />
          </>
        )}
      </div>
      {mutation.error && (
        <p role="alert" className="text-caption text-destructive">
          {t(($) => $.features.save_failed)} {mutation.error.message}
        </p>
      )}
    </article>
  );
}
function FlagHistory({ wsId, flagKey }: { wsId: string; flagKey: string }) {
  const { t } = useT("settings");
  const [open, setOpen] = useState(false);
  return (
    <Sheet open={open} onOpenChange={setOpen}>
      <SheetTrigger render={<Button variant="ghost" size="sm" />}>
        {t(($) => $.features.history)}
      </SheetTrigger>
      <SheetContent className="overflow-y-auto">
        <SheetHeader>
          <SheetTitle>{t(($) => $.features.history)}</SheetTitle>
        </SheetHeader>
        {open && <HistoryPage wsId={wsId} flagKey={flagKey} />}
      </SheetContent>
    </Sheet>
  );
}
function HistoryPage({ wsId, flagKey }: { wsId: string; flagKey: string }) {
  const { t } = useT("settings");
  const [before, setBefore] = useState<string>();
  const query = useQuery(featureFlagAuditOptions(wsId, flagKey, before));
  return (
    <div className="space-y-4 px-4 pb-4">
      <p className="break-all">{flagKey}</p>
      {query.isPending ? (
        <p>{t(($) => $.features.loading)}</p>
      ) : query.isError ? (
        <p role="alert">{t(($) => $.features.load_failed)}</p>
      ) : query.data?.entries.length === 0 ? (
        <p>{t(($) => $.features.no_history)}</p>
      ) : (
        query.data?.entries.map((entry) => (
          <article key={entry.id} className="space-y-1 text-caption">
            <time>{entry.created_at}</time>
            <p>
              {entry.actor_type} {entry.actor_id} · {entry.scope_type}
            </p>
            <p>
              {entry.action} ·{" "}
              {entry.old_enabled == null ? "—" : String(entry.old_enabled)} →{" "}
              {entry.new_enabled == null ? "—" : String(entry.new_enabled)}
            </p>
            <p>
              {t(($) => $.features.locked)}:{" "}
              {entry.old_locked == null ? "—" : String(entry.old_locked)} →{" "}
              {entry.new_locked == null ? "—" : String(entry.new_locked)}
            </p>
            {entry.reason && <p>{entry.reason}</p>}
          </article>
        ))
      )}
      {query.data?.next_before && (
        <Button
          variant="outline"
          onClick={() => setBefore(query.data?.next_before)}
        >
          {t(($) => $.features.older)}
        </Button>
      )}
    </div>
  );
}
