"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertCircle, CheckCircle2, Loader2, ShieldCheck, Trash2 } from "lucide-react";
import { toast } from "sonner";
import {
  connectionBindingsOptions,
  connectionProvidersOptions,
  connectionsOptions,
  useBindConnection,
  useCreateConnection,
  useDeleteConnection,
  useDeleteConnectionSecret,
  useSetConnectionEnabled,
  useSetConnectionSecret,
  useTestConnection,
  useUnbindConnection,
  useUpdateConnection,
} from "@multica/core/connections";
import { useCurrentMember } from "@multica/core/permissions";
import { useCurrentWorkspace } from "@multica/core/paths";
import { pluginInstallationsOptions } from "@multica/core/plugins";
import { agentListOptions } from "@multica/core/workspace/queries";
import type {
  Connection,
  ConnectionConfigField,
  ConnectionConsumerType,
  ConnectionProvider,
} from "@multica/core/types";
import { Alert, AlertDescription, AlertTitle } from "@multica/ui/components/ui/alert";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Switch } from "@multica/ui/components/ui/switch";
import { useT } from "../../i18n";
import {
  SettingsCard,
  SettingsReadOnlyNotice,
  SettingsSection,
  SettingsTab,
} from "./settings-layout";

type Translate = ReturnType<typeof useT<"settings">>["t"];

const CONSUMER_TYPES: ConnectionConsumerType[] = ["plugin_installation", "agent", "native"];

function providerOf(providers: ConnectionProvider[], key: string): ConnectionProvider | undefined {
  return providers.find((provider) => provider.key === key);
}

function statusVariant(status: string): "secondary" | "outline" | "destructive" {
  if (status === "ok") return "secondary";
  if (status === "error" || status === "needs_reauth") return "destructive";
  return "outline";
}

function statusLabel(status: string, t: Translate): string {
  switch (status) {
    case "ok":
      return t(($) => $.connections.status.ok);
    case "degraded":
      return t(($) => $.connections.status.degraded);
    case "error":
      return t(($) => $.connections.status.error);
    case "needs_reauth":
      return t(($) => $.connections.status.needs_reauth);
    default:
      return t(($) => $.connections.status.unchecked);
  }
}

/**
 * The credential form. Host mode writes a value that can never be read back;
 * reference mode writes the pointer an operator's secret store resolves. Both
 * are write-only here, which is why the field starts empty even when a secret
 * is configured.
 */
function SecretForm({ wsId, connection, canManage }: { wsId: string; connection: Connection; canManage: boolean }) {
  const { t } = useT("settings");
  const setSecret = useSetConnectionSecret(wsId);
  const deleteSecret = useDeleteConnectionSecret(wsId);
  const key = "api_key";
  const [value, setValue] = useState("");
  const configured = connection.configured_secrets.includes(key);
  const busy = setSecret.isPending || deleteSecret.isPending;
  const reportError = (error: unknown) =>
    toast.error(error instanceof Error ? error.message : t(($) => $.connections.action_failed));

  return (
    <div className="flex flex-col gap-2 px-4 py-4 sm:flex-row sm:items-center sm:justify-between">
      <div className="min-w-0">
        <div className="text-caption font-medium">{t(($) => $.connections.secret.title)}</div>
        <p className="mt-0.5 text-caption text-muted-foreground">
          {connection.secret_mode === "reference"
            ? t(($) => $.connections.secret.reference_hint)
            : t(($) => $.connections.secret.host_hint)}
        </p>
        <div className="mt-1">
          {configured ? (
            <Badge variant="secondary">{t(($) => $.connections.secret.configured)}</Badge>
          ) : (
            <Badge variant="outline">{t(($) => $.connections.secret.not_configured)}</Badge>
          )}
        </div>
      </div>
      <div className="flex w-full items-center gap-2 sm:w-96">
        <Input
          type={connection.secret_mode === "reference" ? "text" : "password"}
          autoComplete="off"
          disabled={!canManage || busy}
          value={value}
          placeholder={connection.secret_mode === "reference"
            ? t(($) => $.connections.secret.reference_placeholder)
            : configured
              ? t(($) => $.connections.secret.placeholder_set)
              : t(($) => $.connections.secret.placeholder)}
          onChange={(event) => setValue(event.target.value)}
        />
        <Button
          size="sm"
          disabled={!canManage || busy || value.trim().length === 0}
          onClick={() => setSecret
            .mutateAsync({ connectionId: connection.id, request: { key, value } })
            .then(() => {
              setValue("");
              toast.success(t(($) => $.connections.secret.saved));
            })
            .catch(reportError)}
        >
          {setSecret.isPending ? <Loader2 className="animate-spin" /> : null}
          {t(($) => $.connections.secret.save)}
        </Button>
        {configured ? (
          <Button
            size="sm"
            variant="ghost"
            disabled={!canManage || busy}
            onClick={() => deleteSecret
              .mutateAsync({ connectionId: connection.id, key })
              .then(() => toast.success(t(($) => $.connections.secret.removed)))
              .catch(reportError)}
          >
            {t(($) => $.connections.secret.remove)}
          </Button>
        ) : null}
      </div>
    </div>
  );
}

function ConfigFieldInput({
  field,
  value,
  disabled,
  onChange,
}: {
  field: ConnectionConfigField;
  value: unknown;
  disabled: boolean;
  onChange: (value: unknown) => void;
}) {
  if (field.type === "bool") {
    return (
      <Switch
        disabled={disabled}
        checked={value === true}
        onCheckedChange={(checked) => onChange(checked === true)}
      />
    );
  }
  if (field.type === "enum") {
    return (
      <Select
        items={(field.options ?? []).map((option) => ({ value: option, label: option }))}
        value={typeof value === "string" ? value : ""}
        onValueChange={(next) => next && onChange(next)}
      >
        <SelectTrigger disabled={disabled}><SelectValue /></SelectTrigger>
        <SelectContent>
          {(field.options ?? []).map((option) => (
            <SelectItem key={option} value={option}>{option}</SelectItem>
          ))}
        </SelectContent>
      </Select>
    );
  }
  if (field.type === "number") {
    return (
      <Input
        type="number"
        disabled={disabled}
        value={typeof value === "number" ? String(value) : ""}
        placeholder={field.placeholder ?? ""}
        onChange={(event) => {
          const parsed = Number(event.target.value);
          onChange(event.target.value === "" || Number.isNaN(parsed) ? undefined : parsed);
        }}
      />
    );
  }
  return (
    <Input
      disabled={disabled}
      value={typeof value === "string" ? value : ""}
      placeholder={field.placeholder ?? ""}
      onChange={(event) => onChange(event.target.value)}
    />
  );
}

/**
 * The connection's non-secret configuration, generated from the provider
 * schema. The expected revision travels back with the save so a form rendered
 * before someone else's edit is refused instead of overwriting it.
 */
function ConfigForm({
  wsId,
  connection,
  provider,
  canManage,
}: {
  wsId: string;
  connection: Connection;
  provider: ConnectionProvider | undefined;
  canManage: boolean;
}) {
  const { t } = useT("settings");
  const update = useUpdateConnection(wsId);
  const [name, setName] = useState(connection.name);
  const [environment, setEnvironment] = useState(connection.environment);
  const [values, setValues] = useState<Record<string, unknown>>(() => ({ ...connection.config }));
  const fields = provider?.config_schema ?? [];
  if (fields.length === 0 && connection.level === "instance") return null;

  const submit = () => update
    .mutateAsync({
      connectionId: connection.id,
      request: {
        name,
        environment,
        config: values,
        expected_revision: connection.revision,
      },
    })
    .then(() => toast.success(t(($) => $.connections.config.saved)))
    .catch((error: unknown) => toast.error(
      error instanceof Error && error.message.includes("409")
        ? t(($) => $.connections.config.revision_conflict)
        : error instanceof Error
          ? error.message
          : t(($) => $.connections.action_failed),
    ));

  return (
    <div className="space-y-3 px-4 py-4">
      <div className="text-caption font-medium">{t(($) => $.connections.config.title)}</div>
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
        <div className="text-caption font-medium">{t(($) => $.connections.config.name)}</div>
        <div className="w-full sm:w-96">
          <Input disabled={!canManage || update.isPending} value={name} onChange={(event) => setName(event.target.value)} />
        </div>
      </div>
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
        <div className="text-caption font-medium">{t(($) => $.connections.config.environment)}</div>
        <div className="w-full sm:w-96">
          <Select
            items={[
              { value: "production", label: t(($) => $.connections.create.environment_production) },
              { value: "fixture", label: t(($) => $.connections.create.environment_fixture) },
            ]}
            value={environment}
            onValueChange={(next) => next && setEnvironment(next)}
          >
            <SelectTrigger disabled={!canManage || update.isPending}><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="production">{t(($) => $.connections.create.environment_production)}</SelectItem>
              <SelectItem value="fixture">{t(($) => $.connections.create.environment_fixture)}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>
      {fields.map((field) => (
        <div key={field.key} className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
          <div className="min-w-0">
            <div className="text-caption font-medium">
              {field.label}
              {field.required ? <span className="ml-1 text-destructive">*</span> : null}
            </div>
            {field.description ? (
              <p className="mt-0.5 text-caption text-muted-foreground">{field.description}</p>
            ) : null}
          </div>
          <div className="w-full sm:w-96">
            <ConfigFieldInput
              field={field}
              value={values[field.key]}
              disabled={!canManage || update.isPending}
              onChange={(next) => setValues((current) => ({ ...current, [field.key]: next }))}
            />
          </div>
        </div>
      ))}
      <div className="flex justify-end">
        <Button size="sm" disabled={!canManage || update.isPending} onClick={submit}>
          {update.isPending ? <Loader2 className="animate-spin" /> : null}
          {t(($) => $.connections.config.save)}
        </Button>
      </div>
    </div>
  );
}

/** Who may use the connection. Revoking leaves the connection and its secret intact. */
function Bindings({ wsId, connection, canManage }: { wsId: string; connection: Connection; canManage: boolean }) {
  const { t } = useT("settings");
  const bind = useBindConnection(wsId);
  const unbind = useUnbindConnection(wsId);
  const { data: bindings = [], isLoading } = useQuery(connectionBindingsOptions(wsId, connection.id));
  const [consumerType, setConsumerType] = useState<ConnectionConsumerType>("plugin_installation");
  const [consumerId, setConsumerId] = useState("");
  const busy = bind.isPending || unbind.isPending;
  // Plugin installations and agents are chosen by name; a native consumer is a
  // module identifier only the operator knows, so that one stays a text field.
  const { data: installationData } = useQuery({
    ...pluginInstallationsOptions(wsId),
    enabled: consumerType === "plugin_installation",
  });
  const installations = installationData?.plugins ?? [];
  const { data: agents = [] } = useQuery({
    ...agentListOptions(wsId),
    enabled: consumerType === "agent",
  });

  const consumerName = (type: string, id: string) => {
    if (type === "plugin_installation") {
      return installations.find((installation) => installation.id === id)?.name ?? id;
    }
    if (type === "agent") {
      return agents.find((agent) => agent.id === id)?.name ?? id;
    }
    return id;
  };
  const reportError = (error: unknown) =>
    toast.error(error instanceof Error ? error.message : t(($) => $.connections.action_failed));

  const consumerLabel = (type: string) => {
    switch (type) {
      case "plugin_installation":
        return t(($) => $.connections.bindings.plugin_installation);
      case "agent":
        return t(($) => $.connections.bindings.agent);
      default:
        return t(($) => $.connections.bindings.native);
    }
  };

  return (
    <div className="space-y-3 px-4 py-4">
      <div className="text-caption font-medium">{t(($) => $.connections.bindings.title)}</div>
      {isLoading ? (
        <Skeleton className="h-10 w-full" />
      ) : bindings.length === 0 ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.connections.bindings.empty)}</p>
      ) : (
        <ul className="space-y-1.5">
          {bindings.map((binding) => (
            <li key={binding.id} className="flex flex-wrap items-center gap-2 text-caption">
              <Badge variant="outline">{consumerLabel(binding.consumer_type)}</Badge>
              <span>{consumerName(binding.consumer_type, binding.consumer_id)}</span>
              <code className="rounded-xs bg-muted px-1.5 py-0.5 font-mono">{binding.consumer_id}</code>
              <Button
                size="sm"
                variant="ghost"
                className="ml-auto"
                disabled={!canManage || busy}
                onClick={() => unbind
                  .mutateAsync({ connectionId: connection.id, consumerType: binding.consumer_type, consumerId: binding.consumer_id })
                  .then(() => toast.success(t(($) => $.connections.bindings.unbound)))
                  .catch(reportError)}
              >
                {t(($) => $.connections.bindings.unbind)}
              </Button>
            </li>
          ))}
        </ul>
      )}
      {canManage ? (
        <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
          <div className="w-full sm:w-56">
            <Select
              items={CONSUMER_TYPES.map((type) => ({ value: type, label: consumerLabel(type) }))}
              value={consumerType}
              onValueChange={(next) => {
                if (!next) return;
                setConsumerType(next as ConnectionConsumerType);
                setConsumerId("");
              }}
            >
              <SelectTrigger disabled={busy}><SelectValue /></SelectTrigger>
              <SelectContent>
                {CONSUMER_TYPES.map((type) => (
                  <SelectItem key={type} value={type}>{consumerLabel(type)}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          {consumerType === "plugin_installation" ? (
            <div className="w-full sm:w-96">
              <Select
                items={installations.map((installation) => ({ value: installation.id, label: installation.name }))}
                value={consumerId}
                onValueChange={(next) => next && setConsumerId(next)}
              >
                <SelectTrigger disabled={busy}><SelectValue placeholder={t(($) => $.connections.bindings.choose)} /></SelectTrigger>
                <SelectContent>
                  {installations.map((installation) => (
                    <SelectItem key={installation.id} value={installation.id}>{installation.name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          ) : consumerType === "agent" ? (
            <div className="w-full sm:w-96">
              <Select
                items={agents.map((agent) => ({ value: agent.id, label: agent.name }))}
                value={consumerId}
                onValueChange={(next) => next && setConsumerId(next)}
              >
                <SelectTrigger disabled={busy}><SelectValue placeholder={t(($) => $.connections.bindings.choose)} /></SelectTrigger>
                <SelectContent>
                  {agents.map((agent) => (
                    <SelectItem key={agent.id} value={agent.id}>{agent.name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          ) : (
            <Input
              className="w-full sm:w-96"
              disabled={busy}
              value={consumerId}
              placeholder={t(($) => $.connections.bindings.consumer_id_placeholder)}
              onChange={(event) => setConsumerId(event.target.value)}
            />
          )}
          <Button
            size="sm"
            disabled={busy || consumerId.trim().length === 0}
            onClick={() => bind
              .mutateAsync({ connectionId: connection.id, request: { consumer_type: consumerType, consumer_id: consumerId.trim() } })
              .then(() => {
                setConsumerId("");
                toast.success(t(($) => $.connections.bindings.bound));
              })
              .catch(reportError)}
          >
            {bind.isPending ? <Loader2 className="animate-spin" /> : null}
            {t(($) => $.connections.bindings.bind)}
          </Button>
        </div>
      ) : null}
    </div>
  );
}

function ConnectionCard({
  wsId,
  connection,
  providers,
  canManage,
}: {
  wsId: string;
  connection: Connection;
  providers: ConnectionProvider[];
  canManage: boolean;
}) {
  const { t } = useT("settings");
  const provider = providerOf(providers, connection.provider_key);
  const enabledMutation = useSetConnectionEnabled(wsId);
  const deleteMutation = useDeleteConnection(wsId);
  const testMutation = useTestConnection(wsId);
  const busy = enabledMutation.isPending || deleteMutation.isPending || testMutation.isPending;
  const reportError = (error: unknown) =>
    toast.error(error instanceof Error ? error.message : t(($) => $.connections.action_failed));
  const readOnly = connection.read_only || !canManage;

  return (
    <SettingsCard>
      <div className="space-y-3 px-4 py-4">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-body font-semibold">{connection.name}</span>
              <Badge variant={statusVariant(connection.status)}>{statusLabel(connection.status, t)}</Badge>
              {connection.level === "instance" ? (
                <Badge variant="outline">{t(($) => $.connections.level.instance)}</Badge>
              ) : null}
              {!connection.enabled ? (
                <Badge variant="outline">{t(($) => $.connections.disabled)}</Badge>
              ) : null}
            </div>
            <p className="text-caption text-muted-foreground">
              {provider?.display_name ?? connection.provider_key}
              {provider && provider.net_domains.length > 0
                ? ` · ${t(($) => $.connections.reaches, { domains: provider.net_domains.join(", ") })}`
                : ""}
            </p>
            {connection.status_detail ? (
              <p className="mt-1 text-caption text-muted-foreground">{connection.status_detail}</p>
            ) : null}
          </div>
          <div className="flex items-center gap-2">
            {provider?.has_health && connection.secret_mode === "host" ? (
              <Button
                size="sm"
                variant="outline"
                disabled={readOnly || busy}
                onClick={() => testMutation
                  .mutateAsync(connection.id)
                  .then((updated) => {
                    if (updated.status === "ok") toast.success(t(($) => $.connections.test_ok));
                    else toast.error(t(($) => $.connections.test_failed));
                  })
                  .catch(reportError)}
              >
                {testMutation.isPending ? <Loader2 className="animate-spin" /> : <ShieldCheck />}
                {t(($) => $.connections.test)}
              </Button>
            ) : null}
            <Switch
              aria-label={t(($) => $.connections.enabled_label)}
              disabled={readOnly || busy}
              checked={connection.enabled}
              onCheckedChange={(checked) => enabledMutation
                .mutateAsync({ connectionId: connection.id, enabled: checked === true })
                .then(() => toast.success(checked
                  ? t(($) => $.connections.enabled)
                  : t(($) => $.connections.disabled)))
                .catch(reportError)}
            />
            <Button
              size="icon"
              variant="ghost"
              aria-label={t(($) => $.connections.delete)}
              disabled={readOnly || busy}
              onClick={() => deleteMutation
                .mutateAsync(connection.id)
                .then(() => toast.success(t(($) => $.connections.deleted)))
                .catch(reportError)}
            >
              {deleteMutation.isPending ? <Loader2 className="animate-spin" /> : <Trash2 />}
            </Button>
          </div>
        </div>
      </div>
      {connection.level === "workspace" ? (
        <>
          <SecretForm wsId={wsId} connection={connection} canManage={canManage} />
          <ConfigForm wsId={wsId} connection={connection} provider={provider} canManage={canManage} />
          <Bindings wsId={wsId} connection={connection} canManage={canManage} />
        </>
      ) : null}
    </SettingsCard>
  );
}

function CreateConnection({ wsId, providers }: { wsId: string; providers: ConnectionProvider[] }) {
  const { t } = useT("settings");
  const create = useCreateConnection(wsId);
  const [providerKey, setProviderKey] = useState(providers[0]?.key ?? "");
  const [name, setName] = useState("");
  const [secretMode, setSecretMode] = useState<"host" | "reference">("host");
  const reportError = (error: unknown) =>
    toast.error(error instanceof Error ? error.message : t(($) => $.connections.action_failed));

  return (
    <SettingsSection title={t(($) => $.connections.create.title)}>
      <SettingsCard>
        <div className="space-y-3 px-4 py-4">
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
            <div className="text-caption font-medium">{t(($) => $.connections.create.provider)}</div>
            <div className="w-full sm:w-96">
              <Select
                items={providers.map((provider) => ({ value: provider.key, label: provider.display_name }))}
                value={providerKey}
                onValueChange={(next) => next && setProviderKey(next)}
              >
                <SelectTrigger disabled={create.isPending}><SelectValue /></SelectTrigger>
                <SelectContent>
                  {providers.map((provider) => (
                    <SelectItem key={provider.key} value={provider.key}>{provider.display_name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
            <div className="text-caption font-medium">{t(($) => $.connections.create.name)}</div>
            <div className="w-full sm:w-96">
              <Input
                disabled={create.isPending}
                value={name}
                placeholder={t(($) => $.connections.create.name_placeholder)}
                onChange={(event) => setName(event.target.value)}
              />
            </div>
          </div>
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
            <div className="text-caption font-medium">{t(($) => $.connections.create.secret_mode)}</div>
            <div className="w-full sm:w-96">
              <Select
                items={[
                  { value: "host", label: t(($) => $.connections.create.secret_mode_host) },
                  { value: "reference", label: t(($) => $.connections.create.secret_mode_reference) },
                ]}
                value={secretMode}
                onValueChange={(next) => next && setSecretMode(next as "host" | "reference")}
              >
                <SelectTrigger disabled={create.isPending}><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="host">{t(($) => $.connections.create.secret_mode_host)}</SelectItem>
                  <SelectItem value="reference">{t(($) => $.connections.create.secret_mode_reference)}</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="flex justify-end">
            <Button
              size="sm"
              disabled={create.isPending || name.trim().length === 0 || providerKey.length === 0}
              onClick={() => create
                .mutateAsync({ provider_key: providerKey, name: name.trim(), secret_mode: secretMode })
                .then(() => {
                  setName("");
                  toast.success(t(($) => $.connections.create.created));
                })
                .catch(reportError)}
            >
              {create.isPending ? <Loader2 className="animate-spin" /> : null}
              {t(($) => $.connections.create.submit)}
            </Button>
          </div>
        </div>
      </SettingsCard>
    </SettingsSection>
  );
}

export function ConnectionsTab() {
  const { t } = useT("settings");
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const { role } = useCurrentMember(wsId);
  const canManage = role === "owner" || role === "admin";

  const providers = useQuery(connectionProvidersOptions(wsId));
  const { data, isLoading, isError } = useQuery(connectionsOptions(wsId));
  const connections = data?.connections ?? [];

  return (
    <SettingsTab
      title={t(($) => $.connections.title)}
      description={t(($) => $.connections.description)}
      scope="workspace"
    >
      {role && !canManage ? <SettingsReadOnlyNotice wsId={wsId} /> : null}

      {canManage && (providers.data?.length ?? 0) > 0 ? (
        <CreateConnection wsId={wsId} providers={providers.data ?? []} />
      ) : null}

      <SettingsSection title={t(($) => $.connections.installed.title)}>
        {isLoading ? (
          <Skeleton className="h-24 w-full" aria-label={t(($) => $.connections.loading)} />
        ) : isError ? (
          <Alert variant="destructive">
            <AlertCircle />
            <AlertTitle>{t(($) => $.connections.load_failed)}</AlertTitle>
            <AlertDescription>{t(($) => $.connections.load_failed_description)}</AlertDescription>
          </Alert>
        ) : connections.length === 0 ? (
          <SettingsCard>
            <p className="flex items-center gap-2 px-4 py-6 text-caption text-muted-foreground">
              <CheckCircle2 className="size-4" />
              {t(($) => $.connections.empty)}
            </p>
          </SettingsCard>
        ) : (
          <div className="space-y-4">
            {connections.map((connection) => (
              <ConnectionCard
                key={connection.id}
                wsId={wsId}
                connection={connection}
                providers={providers.data ?? []}
                canManage={canManage}
              />
            ))}
          </div>
        )}
      </SettingsSection>
    </SettingsTab>
  );
}
