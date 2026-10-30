"use client";
import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import {
  Button,
  Input,
  Select,
  SelectItem,
  RadioGroup,
  Radio,
  Chip,
  Skeleton,
  addToast,
} from "@heroui/react";
import {
  ArrowDown,
  ArrowUpRight,
  ArrowDownToLine,
  Check,
  Edit3,
  GitBranch,
  LifeBuoy,
  Network,
  Power,
  Plus,
  RefreshCw,
  Search,
  ShieldCheck,
  SlidersHorizontal,
} from "lucide-react";
import { en as t } from "@/i18n/en";
import { pricingEn as p } from "@/i18n/pricing-en";
import { safeReferenceURL } from "@/lib/pricing";
import { ModelPriceUpdate } from "./model-price-update";
import { consoleEn as c } from "@/i18n/console-en";
import { backupEn as b } from "@/i18n/backup-en";
import { imageEn as im } from "@/i18n/image-en";
import { longContextEn as lc } from "@/i18n/long-context-en";
import { limitsEn as lm } from "@/i18n/limits-en";
import { isImageUpstream, isPriced } from "@/lib/images";
import { compactTokens, longContextFormKeys, longContextRates, maxLongContextThreshold } from "@/lib/long-context";
import { api, count, date, dollars, errorMessage, money } from "@/lib/api";
import { backupRoutes, isBackup, type BackupRoute } from "@/lib/backups";
import type { BackupBalance, Credential, Endpoint, Model, Proxy, ReportedLimit, Snapshot } from "@/lib/types";
import { useConsole } from "./console-provider";
import { BackupForm } from "./backup-form";
import { CredentialForm } from "./credential-form";
import { ModelImport } from "./model-import";
import {
  PageHeader,
  DataTable,
  Empty,
  IconButton,
  Status,
  Confirm,
  ErrorBox,
} from "./shared";
import { ResourceForm, type Field, type FormValues } from "./resource-form";
const providers = [
  { key: "openai", label: t.openai },
  { key: "anthropic", label: t.anthropic },
];
const modelList = (value: unknown) =>
  String(value ?? "")
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
const enabledField: Field = {
  key: "enabled",
  label: t.enabled,
  type: "switch",
};
interface FormConfig {
  title: string;
  description?: string;
  fields: Field[];
  initial: FormValues;
  onSubmit: (values: FormValues) => Promise<void>;
  onChange?: (key: string, value: string, values: FormValues) => FormValues;
}
// A key's local limits, with the limits its provider last reported for the
// models it served, so the operator can match them to the key's real tier.
function CredentialLimits({ credential, reported }: { credential: Credential; reported: ReportedLimit[] }) {
  const shown = [...reported].sort((a, b) => (b.tokens ?? b.input_tokens ?? 0) - (a.tokens ?? a.input_tokens ?? 0)).slice(0, 3);
  return (
    <div className="tabular-nums">
      <p className="whitespace-nowrap">
        {lm.local
          .replace("{rpm}", count(credential.rpm))
          .replace("{tpm}", compactTokens(credential.tpm))
          .replace("{concurrency}", count(credential.max_concurrency))}
      </p>
      {shown.map((limit) => (
        <p key={limit.model} className="mt-1 text-xs text-default-500" title={lm.reportedTitle.replace("{date}", date(limit.observed_at))}>
          {(limit.input_tokens || limit.output_tokens ? lm.reportedSplit : lm.reported)
            .replace("{model}", limit.model)
            .replace("{rpm}", limit.requests ? count(limit.requests) : "—")
            .replace("{tpm}", limit.tokens ? compactTokens(limit.tokens) : "—")
            .replace("{input}", limit.input_tokens ? compactTokens(limit.input_tokens) : "—")
            .replace("{output}", limit.output_tokens ? compactTokens(limit.output_tokens) : "—")}
        </p>
      ))}
      {reported.length > shown.length && (
        <p className="mt-1 text-xs text-default-400">{lm.reportedMore.replace("{count}", count(reported.length - shown.length))}</p>
      )}
    </div>
  );
}
export function Upstreams() {
  const { state, refresh } = useConsole();
  const [form, setForm] = useState<FormConfig | null>(null);
  const [credentialDialog, setCredentialDialog] = useState<{
    credential?: Credential;
  } | null>(null);
  const [disable, setDisable] = useState<{
    name: string;
    run: () => Promise<void>;
  } | null>(null);
  const [busy, setBusy] = useState("");
  const [backupDialog, setBackupDialog] = useState<{
    backup?: BackupRoute;
  } | null>(null);
  // Backups have their own section; the generic tables list primary routes.
  const backups = backupRoutes(state.endpoints, state.credentials);
  const backupEndpointIDs = new Set(backups.map((route) => route.endpoint.id));
  const endpoints = (state.endpoints ?? []).filter((endpoint) => !isBackup(endpoint));
  const credentials = (state.credentials ?? []).filter(
    (credential) => !backupEndpointIDs.has(credential.endpoint_id),
  );
  const [balances, setBalances] = useState<Record<string, BackupBalance>>({});
  const [loadedSignature, setLoadedSignature] = useState("");
  const [refreshingBalances, setRefreshingBalances] = useState(false);
  const fetchBalances = useCallback(
    (refresh: boolean) =>
      api<{ balances: BackupBalance[] }>("/api/admin/backups/balances", { refresh }).then((data) =>
        setBalances(Object.fromEntries(data.balances.map((balance) => [balance.vendor, balance]))),
      ),
    [],
  );
  // Read balances whenever a backup is added, removed or reconfigured.
  const backupSignature = backups
    .map((route) => `${route.vendor}:${route.credential?.masked_key}:${route.credential?.proxy_id}:${route.endpoint.management_key}`)
    .join("|");
  useEffect(() => {
    if (!backupSignature) return;
    let active = true;
    fetchBalances(false)
      .catch(() => undefined)
      .finally(() => {
        if (active) setLoadedSignature(backupSignature);
      });
    return () => {
      active = false;
    };
  }, [backupSignature, fetchBalances]);
  const balancesLoading = refreshingBalances || (!!backupSignature && loadedSignature !== backupSignature);
  async function refreshBalances() {
    setRefreshingBalances(true);
    try {
      await fetchBalances(true);
    } catch (e) {
      addToast({ title: errorMessage(e), color: "danger" });
    } finally {
      setRefreshingBalances(false);
    }
  }
  const needsModels = credentials.some((credential) => {
    const endpoint = state.endpoints?.find(
      (host) => host.id === credential.endpoint_id,
    );
    return (
      credential.enabled &&
      endpoint?.enabled &&
      !state.models.some(
        (model) => model.enabled && model.provider === endpoint.provider,
      )
    );
  });
  async function save(path: string, body: unknown) {
    await api(`/api/admin/${path}`, body);
    await refresh();
    addToast({ title: t.saved, color: "success" });
  }
  async function toggleBackup(route: BackupRoute) {
    const enabled = !!route.credential?.enabled && route.endpoint.enabled;
    // proxy_id is authoritative on the backup API, so resend the saved exit.
    const run = () =>
      save("backups", { vendor: route.vendor, enabled: !enabled, proxy_id: route.credential?.proxy_id ?? "" });
    if (enabled) {
      setDisable({ name: route.name, run });
      return;
    }
    setBusy(route.endpoint.id);
    try {
      await run();
    } catch (e) {
      addToast({ title: errorMessage(e), color: "danger" });
    } finally {
      setBusy("");
    }
  }
  function hostForm(endpoint?: Endpoint) {
    setForm({
      title: endpoint ? t.editHost : t.addHost,
      description: t.apiHostHelp,
      fields: [
        { key: "name", label: t.name, required: true },
        {
          key: "provider",
          label: t.provider,
          type: "select",
          options: providers,
          required: true,
        },
        { key: "base_url", label: t.baseURL, required: true },
        {
          key: "auth_mode",
          label: t.authMode,
          type: "select",
          options: [
            { key: "bearer", label: t.bearer },
            { key: "x-api-key", label: t.apiKeyHeader },
          ],
        },
        {
          key: "models",
          label: t.allowedModels,
          description: t.endpointModelsHelp,
        },
        enabledField,
      ],
      initial: endpoint
        ? { ...endpoint, models: endpoint.models.join(", ") }
        : {
            name: "",
            provider: "openai",
            base_url: "https://api.openai.com",
            auth_mode: "bearer",
            models: "",
            enabled: true,
          },
      onChange: (key, value, values) =>
        key === "provider"
          ? {
              ...values,
              base_url:
                value === "openai"
                  ? "https://api.openai.com"
                  : "https://api.anthropic.com",
              auth_mode: value === "openai" ? "bearer" : "x-api-key",
            }
          : values,
      onSubmit: async (values) => {
        try {
          const url = new URL(String(values.base_url));
          if (!["https:", "http:"].includes(url.protocol)) throw new Error();
        } catch {
          throw new Error(t.urlRequired);
        }
        await save("endpoints", {
          ...values,
          id: endpoint?.id,
          models: modelList(values.models),
        });
      },
    });
  }
  function credentialForm(credential?: Credential) {
    setCredentialDialog({ credential });
  }
  async function toggle(kind: string, row: Endpoint | Credential) {
    const run = async () => {
      await save(kind, { ...row, enabled: !row.enabled });
    };
    if (row.enabled) {
      setDisable({ name: row.name, run });
      return;
    }
    setBusy(row.id);
    try {
      await run();
    } catch (e) {
      addToast({ title: errorMessage(e), color: "danger" });
    } finally {
      setBusy("");
    }
  }
  return (
    <>
      <PageHeader
        title={t.upstreams}
        description={t.upstreamsDescription}
        action={
          <div className="rp-management-actions">
            <Button variant="bordered" startContent={<Plus size={15} />} onPress={() => hostForm()}>{t.addHost}</Button>
            <Button color="primary" startContent={<Plus size={15} />} onPress={() => credentialForm()}>{t.addCredential}</Button>
          </div>
        }
      />
      <div className="rp-console-resource-summary">
        {[
          { label: c.hostCount, value: endpoints.length },
          { label: c.credentialCount, value: credentials.length },
          { label: c.proxyCount, value: state.proxies?.length ?? 0 },
        ].map((resource) => (
          <div key={resource.label}>
            <span>{resource.label}</span>
            <strong>
              {count(resource.value)} <small>{c.resourceCount}</small>
            </strong>
          </div>
        ))}
      </div>
      {needsModels && (
        <div className="mb-6 flex flex-wrap items-center justify-between gap-4 rounded-[3px] border border-default-200 p-5">
          <div>
            <h2 className="text-sm font-medium">{t.modelsNextStepTitle}</h2>
            <p className="mt-2 max-w-2xl text-sm leading-6 text-default-500">
              {t.modelsNextStepBody}
            </p>
          </div>
          <Button as={Link} href="/app/models" variant="bordered">
            {t.configureModels}
          </Button>
        </div>
      )}
      <div className="rp-management-section-heading"><h2>{t.hosts}<span>{count(endpoints.length)}</span></h2></div>
      <DataTable
        caption={t.hosts}
        columns={[t.name, t.provider, t.baseURL, t.status, t.actions]}
        rows={endpoints.map((endpoint) => [
          endpoint.name,
          <span key="provider" className="rp-management-provider"><i aria-hidden="true" data-provider={endpoint.provider}>{endpoint.provider === "anthropic" ? "A" : "O"}</i>{endpoint.provider === "anthropic" ? t.anthropic : t.openai}</span>,
          <code key="url" className="text-xs">
            {endpoint.base_url}
          </code>,
          <Status key="enabled" enabled={endpoint.enabled} />,
          <div key="actions" className="flex">
            <IconButton label={t.edit} onPress={() => hostForm(endpoint)}>
              <Edit3 size={16} />
            </IconButton>
            <IconButton
              label={endpoint.enabled ? t.disable : t.enable}
              isLoading={busy === endpoint.id}
              onPress={() => void toggle("endpoints", endpoint)}
            >
              <Power size={16} />
            </IconButton>
          </div>,
        ])}
        empty={
          <Empty
            title={t.noHosts}
            body={t.noHostsBody}
            action={
              <Button color="primary" onPress={() => hostForm()}>
                {t.addHost}
              </Button>
            }
          />
        }
      />
      <div className="rp-management-section-heading rp-management-section-spaced">
        <h2>{t.credentials}<span>{count(credentials.length)}</span></h2>
        <IconButton label={t.addCredential} onPress={() => credentialForm()}>
          <Plus size={18} />
        </IconButton>
      </div>
      <DataTable
        caption={t.credentials}
        columns={[
          t.name,
          t.endpoint,
          t.key,
          t.budgetAvailable,
          lm.limits,
          t.proxy,
          t.status,
          t.actions,
        ]}
        rows={credentials.map((credential) => [
          <div key="name">
            <p>{credential.name}</p>
            <p className="mt-1 text-xs text-default-500">
              {credential.quota_group || t.emptyValue}
            </p>
          </div>,
          <div key="endpoint">
            <p>
              {state.endpoints?.find((e) => e.id === credential.endpoint_id)
                ?.name || credential.endpoint_id}
            </p>
            {credential.workspace_id && (
              <p className="mt-1 max-w-48 break-all font-mono text-xs text-default-500">
                {credential.workspace_id}
              </p>
            )}
          </div>,
          <code key="secret" className="text-xs">
            {credential.masked_key}
          </code>,
          <div key="budget" className="tabular-nums">
            <p>
              {credential.budget_micros
                ? money(
                    Math.max(
                      0,
                      credential.budget_micros -
                        credential.spent_micros -
                        credential.held_micros,
                    ),
                  )
                : t.unlimited}
            </p>
            <p className="mt-1 text-xs text-default-500">
              {t.spent}: {money(credential.spent_micros, true)}
            </p>
          </div>,
          <CredentialLimits key="limits" credential={credential} reported={state.reported_limits?.[credential.id] ?? []} />,
          state.proxies?.find((p) => p.id === credential.proxy_id)?.name ||
            t.direct,
          <Status key="enabled" enabled={credential.enabled} />,
          <div key="actions" className="flex">
            <IconButton
              label={t.edit}
              onPress={() => credentialForm(credential)}
            >
              <Edit3 size={16} />
            </IconButton>
            <IconButton
              label={credential.enabled ? t.disable : t.enable}
              isLoading={busy === credential.id}
              onPress={() => void toggle("credentials", credential)}
            >
              <Power size={16} />
            </IconButton>
          </div>,
        ])}
        empty={
          <Empty
            title={t.noCredentials}
            body={t.noCredentialsBody}
            action={
              <Button variant="flat" onPress={() => credentialForm()}>
                {t.addCredential}
              </Button>
            }
          />
        }
      />
      <div className="rp-management-section-heading rp-management-section-spaced">
        <h2>{b.section}<span>{count(backups.length)}</span></h2>
        <div className="flex">
          {backups.length > 0 && (
            <IconButton label={b.refreshBalances} isLoading={refreshingBalances} onPress={() => void refreshBalances()}>
              <RefreshCw size={16} />
            </IconButton>
          )}
          {backups.length < 2 && (
            <IconButton label={b.add} onPress={() => setBackupDialog({})}>
              <Plus size={18} />
            </IconButton>
          )}
        </div>
      </div>
      <p className="mb-4 max-w-3xl text-xs leading-6 text-default-500">{b.sectionHelp}</p>
      <DataTable
        caption={b.section}
        columns={[t.provider, t.key, b.balance, b.order, t.rpm, t.proxy, t.status, t.actions]}
        rows={backups.map((route, index) => [
          <span key="vendor" className="font-medium">{route.name}</span>,
          <code key="secret" className="text-xs">
            {route.credential?.masked_key || t.emptyValue}
          </code>,
          <BackupBalanceCell key="balance" balance={balances[route.vendor]} loading={balancesLoading && !balances[route.vendor]} />,
          <span key="order" className="tabular-nums">{index + 1}</span>,
          count(route.credential?.rpm ?? 0),
          state.proxies?.find((p) => p.id === route.credential?.proxy_id)?.name || t.direct,
          <Status key="enabled" enabled={!!route.credential?.enabled && route.endpoint.enabled} />,
          <div key="actions" className="flex">
            <IconButton label={t.edit} onPress={() => setBackupDialog({ backup: route })}>
              <Edit3 size={16} />
            </IconButton>
            <IconButton
              label={route.credential?.enabled && route.endpoint.enabled ? t.disable : t.enable}
              isLoading={busy === route.endpoint.id}
              onPress={() => void toggleBackup(route)}
            >
              <Power size={16} />
            </IconButton>
          </div>,
        ])}
        empty={
          <Empty
            title={b.emptyTitle}
            body={b.emptyBody}
            action={
              <Button variant="flat" onPress={() => setBackupDialog({})}>
                {b.add}
              </Button>
            }
          />
        }
      />
      <div className="rp-console-inline-note">
        <LifeBuoy size={19} strokeWidth={1.6} />
        <div>
          <h2>{b.noteTitle}</h2>
          <p>{b.scopeNote} {b.spendNote}</p>
        </div>
      </div>
      {backupDialog && (
        <BackupForm
          backup={backupDialog.backup}
          configured={backups.map((route) => route.vendor)}
          proxies={state.proxies || []}
          onClose={() => setBackupDialog(null)}
          onSave={async (body) => {
            await api("/api/admin/backups", body);
            await refresh();
            void fetchBalances(false).catch(() => undefined);
            addToast({ title: b.savedToast, color: "success" });
          }}
        />
      )}
      {credentialDialog && (
        <CredentialForm
          credential={credentialDialog.credential}
          endpoints={endpoints}
          proxies={state.proxies || []}
          onClose={() => setCredentialDialog(null)}
          onSave={(body) => save("credentials", body)}
          onAddHost={() => {
            setCredentialDialog(null);
            hostForm();
          }}
        />
      )}
      {form && <ResourceForm {...form} onClose={() => setForm(null)} />}{" "}
      {disable && (
        <Confirm
          title={t.disabledConfirmTitle}
          body={`${disable.name}: ${t.disabledConfirmBody}`}
          action={t.disable}
          onClose={() => setDisable(null)}
          onConfirm={disable.run}
        />
      )}
    </>
  );
}
// The latest vendor balance of one backup: the account balance when a
// management key is saved, and an OpenRouter key's own remaining cap.
function BackupBalanceCell({ balance, loading }: { balance?: BackupBalance; loading: boolean }) {
  if (loading) return <Skeleton className="h-4 w-20 rounded-sm" />;
  if (!balance) return <span className="text-default-400">{t.emptyValue}</span>;
  const updated = b.balanceUpdated.replace(
    "{time}",
    new Date(balance.checked_at).toLocaleTimeString("en-US", { hour: "2-digit", minute: "2-digit" }),
  );
  const keyLimit =
    balance.key_limit_remaining_micros !== undefined
      ? b.keyLimitLeft.replace("{amount}", money(balance.key_limit_remaining_micros))
      : "";
  if (balance.error)
    return <p className="w-56 whitespace-normal text-xs leading-5 text-warning-700 dark:text-warning-400">{balance.error}</p>;
  if (balance.balance_micros === undefined)
    return (
      <div className="w-56 whitespace-normal text-xs leading-5 text-default-500">
        {keyLimit && <p className="text-sm text-foreground tabular-nums">{keyLimit}</p>}
        <p>{b.balanceNeedsKey}</p>
      </div>
    );
  return (
    <div className="tabular-nums">
      <p>{money(balance.balance_micros)}</p>
      <p className="mt-1 text-xs text-default-500">{keyLimit || updated}</p>
    </div>
  );
}
export function Proxies() {
  const { state, refresh } = useConsole();
  const [form, setForm] = useState<FormConfig | null>(null);
  const [disable, setDisable] = useState<Proxy | null>(null);
  const [busy, setBusy] = useState("");
  async function save(body: unknown) {
    await api("/api/admin/proxies", body);
    await refresh();
    addToast({ title: t.saved, color: "success" });
  }
  function open(proxy?: Proxy) {
    setForm({
      title: proxy ? t.editProxy : t.addProxy,
      fields: [
        { key: "name", label: t.name, required: true },
        {
          key: "url",
          label: t.proxyURL,
          type: "password",
          required: !proxy,
          description: proxy ? t.preserveProxy : t.proxyHelp,
        },
        enabledField,
      ],
      initial: {
        name: proxy?.name ?? "",
        url: "",
        enabled: proxy?.enabled ?? true,
      },
      onSubmit: async (values) => {
        if (values.url) {
          try {
            const url = new URL(String(values.url));
            if (
              !["socks5:", "socks5h:"].includes(url.protocol) ||
              !url.hostname ||
              !url.port
            )
              throw new Error();
          } catch {
            throw new Error(t.proxyURLInvalid);
          }
        }
        await save({ ...values, id: proxy?.id });
      },
    });
  }
  return (
    <>
      <PageHeader
        title={t.proxies}
        description={t.proxiesDescription}
        action={
          <Button color="primary" onPress={() => open()}>
            {t.addProxy}
          </Button>
        }
      />
      <DataTable
        caption={t.proxies}
        columns={[t.name, t.connection, t.status, t.actions]}
        rows={(state.proxies ?? []).map((proxy) => [
          proxy.name,
          <code key="url" className="text-xs">
            {proxy.masked_url}
          </code>,
          <Status key="enabled" enabled={proxy.enabled} />,
          <div key="actions" className="flex">
            <IconButton label={t.edit} onPress={() => open(proxy)}>
              <Edit3 size={16} />
            </IconButton>
            <IconButton
              label={proxy.enabled ? t.disable : t.enable}
              isLoading={busy === proxy.id}
              onPress={() => {
                if (proxy.enabled) setDisable(proxy);
                else {
                  setBusy(proxy.id);
                  void save({
                    id: proxy.id,
                    name: proxy.name,
                    url: "",
                    enabled: true,
                  })
                    .catch((e) =>
                      addToast({ title: errorMessage(e), color: "danger" }),
                    )
                    .finally(() => setBusy(""));
                }
              }}
            >
              <Power size={16} />
            </IconButton>
          </div>,
        ])}
        empty={
          <Empty
            title={t.noProxies}
            body={t.noProxiesBody}
            action={
              <Button color="primary" onPress={() => open()}>
                {t.addProxy}
              </Button>
            }
          />
        }
      />
      <div className="rp-console-inline-note">
        <ShieldCheck size={19} strokeWidth={1.6} />
        <div>
          <h2>{c.proxySafety}</h2>
          <p>{t.proxyPolicy}</p>
        </div>
      </div>
      {form && <ResourceForm {...form} onClose={() => setForm(null)} />}{" "}
      {disable && (
        <Confirm
          title={t.disabledConfirmTitle}
          body={`${disable.name}: ${t.disabledConfirmBody}`}
          action={t.disable}
          onClose={() => setDisable(null)}
          onConfirm={() =>
            save({
              id: disable.id,
              name: disable.name,
              url: "",
              enabled: false,
            })
          }
        />
      )}
    </>
  );
}
const rateFields = [
  "input_rate",
  "output_rate",
  "cached_rate",
  "cache_write_rate",
  "cost_input_rate",
  "cost_output_rate",
  "cost_cached_rate",
  "cost_cache_write_rate",
];
const rateLabels = [
  t.inputRate,
  t.outputRate,
  t.cachedRate,
  t.cacheWriteRate,
  t.costInputRate,
  t.costOutputRate,
  t.costCachedRate,
  t.costCacheWriteRate,
];
const imageRateFields = ["image_input_rate", "image_output_rate", "cost_image_input_rate", "cost_image_output_rate"];
const imageRateLabels = [im.imageInputRate, im.imageOutputRate, im.costImageInputRate, im.costImageOutputRate];
const routesImages = (values: Record<string, unknown>) =>
  isImageUpstream(String(values.provider ?? ""), String(values.upstream_model ?? ""));
// The rates an enabled model needs. Image models publish no text output price.
const requiredRates = (values: Record<string, unknown>) =>
  routesImages(values)
    ? ["input_rate", "cost_input_rate", ...imageRateFields]
    : ["input_rate", "output_rate", "cost_input_rate", "cost_output_rate"];

const longRateLabels = [
  lc.longInputRate,
  lc.longOutputRate,
  lc.longCachedRate,
  lc.longCacheWriteRate,
  lc.costLongInputRate,
  lc.costLongOutputRate,
  lc.costLongCachedRate,
  lc.costLongCacheWriteRate,
];

// The models table keeps one set of columns. An image model shows its text and
// image prices together: input, output, cached input, and no cache writes. A
// text model with a long context price shows it under each standard rate.
function modelRateCells(model: Model) {
  if (!isPriced(model))
    return [0, 1, 2, 3].map((i) => <span key={i} className="rp-management-rate">{t.notPriced}</span>);
  if (model.kind !== "image") {
    const long = model.long_context;
    const longRates = long ? [long.input_rate, long.output_rate, long.cached_rate, long.cache_write_rate] : [];
    return [model.input_rate, model.output_rate, model.cached_rate, model.cache_write_rate].map((rate, i) =>
      long ? (
        <span key={i} className="rp-management-rate flex flex-col gap-0.5">
          <span>{money(rate, true)}</span>
          <small className="text-xs text-default-500">
            {lc.over.replace("{tokens}", compactTokens(long.above_input_tokens))}: {money(longRates[i], true)}
          </small>
        </span>
      ) : (
        <span key={i} className="rp-management-rate">{money(rate, true)}</span>
      ),
    );
  }
  const line = (rate: number, label: string) => (
    <span key={label} className="flex items-baseline gap-1.5">
      <span>{money(rate, true)}</span>
      <small className="text-xs text-default-500">{label}</small>
    </span>
  );
  return [
    <span key="input" className="rp-management-rate flex flex-col gap-0.5">{line(model.input_rate, im.text)}{line(model.image_input_rate ?? 0, im.image)}</span>,
    <span key="output" className="rp-management-rate flex flex-col gap-0.5">{model.output_rate > 0 && line(model.output_rate, im.text)}{line(model.image_output_rate ?? 0, im.image)}</span>,
    <span key="cached" className="rp-management-rate">{money(model.cached_rate, true)}</span>,
    <span key="write" className="rp-management-rate text-default-400">{im.notApplicable}</span>,
  ];
}
export function Models() {
  const { state, refresh } = useConsole();
  const operator = state.surface === "admin" && state.user.operator;
  const [importing, setImporting] = useState(false);
  const [updatingPrices, setUpdatingPrices] = useState(false);
  const [form, setForm] = useState<FormConfig | null>(null);
  const [disable, setDisable] = useState<Model | null>(null);
  const [busy, setBusy] = useState("");
  const [search, setSearch] = useState("");
  const [provider, setProvider] = useState("all");
  const [status, setStatus] = useState("all");
  async function save(body: Model | Record<string, unknown>) {
    if (
      body.enabled &&
      requiredRates(body as Record<string, unknown>).some(
        (key) =>
          !Number.isFinite(Number(body[key as keyof typeof body])) ||
          Number(body[key as keyof typeof body]) <= 0,
      )
    )
      throw new Error(t.modelPositiveRates);
    await api("/api/admin/models", body);
    await refresh();
    addToast({ title: t.saved, color: "success" });
  }
  function open(model?: Model) {
    // Long context rates are edited with the other manual rates. A verified
    // reference keeps the provider's published long context price.
    const editsLongContext = (values: FormValues) =>
      !routesImages(values) && (!model?.pricing_reference || !!values.manual_pricing);
    setForm({
      title: model ? t.editModel : t.addModel,
      description: `${p.balanceHelp} ${p.manualHelp}`,
      fields: [
        {
          key: "id",
          label: t.modelID,
          required: true,
          readOnly: !!model,
          validate: (value) =>
            !model &&
            state.models.some((existing) => existing.id === value.trim())
              ? t.modelIDExists
              : undefined,
        },
        { key: "name", label: t.name, required: true },
        {
          key: "provider",
          label: t.provider,
          type: "select",
          options: providers,
          required: true,
          validate: (value, values) => model?.pricing_reference && !values.manual_pricing && value !== model.provider ? p.manualRequired : undefined,
        },
        { key: "upstream_model", label: t.upstreamModel, required: true,
          validate: (value, values) => model?.pricing_reference && !values.manual_pricing && value !== model.upstream_model ? p.manualRequired : undefined,
        },
        {
          key: "manual_pricing", label: p.useManual, type: "switch",
          description: p.manualHelp, visibleWhen: () => !!model?.pricing_reference,
        },
        ...rateFields.map(
          (key, i): Field => ({
            key,
            label: rateLabels[i],
            type: "number",
            required: true,
            visibleWhen: (values) => !model?.pricing_reference || !!values.manual_pricing,
            description: i === 0 ? t.modelExplicitCacheRates : undefined,
            validate: (value, values) =>
              values.enabled &&
              requiredRates(values).includes(key) &&
              (!Number.isFinite(Number(value)) || dollars(value) <= 0)
                ? t.modelPositiveRate
                : undefined,
            section: i === 0 ? t.salesRates : i === 4 ? t.costRates : undefined,
          }),
        ),
        ...imageRateFields.map(
          (key, i): Field => ({
            key,
            label: imageRateLabels[i],
            type: "number",
            required: true,
            visibleWhen: (values) => routesImages(values) && (!model?.pricing_reference || !!values.manual_pricing),
            description: i === 0 ? im.imageRatesHelp : undefined,
            validate: (value, values) =>
              values.enabled && (!Number.isFinite(Number(value)) || dollars(value) <= 0) ? t.modelPositiveRate : undefined,
            section: i === 0 ? im.imageRates : undefined,
          }),
        ),
        {
          key: "long_context_enabled",
          label: lc.longContextEnabled,
          type: "switch",
          description: lc.longContextHelp,
          section: lc.longContextRates,
          visibleWhen: (values) => editsLongContext(values),
        },
        {
          key: "long_context_above",
          label: lc.longContextAbove,
          type: "number",
          min: 1,
          required: true,
          visibleWhen: (values) => editsLongContext(values) && !!values.long_context_enabled,
          validate: (value) =>
            !Number.isInteger(Number(value)) || Number(value) < 1 || Number(value) > maxLongContextThreshold
              ? lc.thresholdRange
              : undefined,
        },
        ...longContextRates.map(
          ({ field, standard }, i): Field => ({
            key: field,
            label: longRateLabels[i],
            type: "number",
            required: true,
            visibleWhen: (values) => editsLongContext(values) && !!values.long_context_enabled,
            validate: (value, values) =>
              !Number.isFinite(Number(value))
                ? undefined
                : i % 4 < 2 && dollars(value) <= 0
                  ? lc.positiveRate
                  : dollars(value) < dollars(values[standard])
                    ? lc.belowStandard
                    : undefined,
          }),
        ),
        {
          key: "max_output_tokens",
          label: t.maxOutput,
          type: "number",
          min: 1,
          required: true,
          validate: (value) =>
            !Number.isInteger(Number(value)) ||
            Number(value) < 1 ||
            Number(value) > 65536
              ? t.modelOutputCapRange
              : undefined,
        },
        enabledField,
      ],
      initial: model
        ? {
            id: model.id,
            name: model.name,
            provider: model.provider,
            upstream_model: model.upstream_model,
            enabled: model.enabled,
            billing_mode: "metered",
            manual_pricing: !model.pricing_reference,
            max_output_tokens: model.max_output_tokens,
            ...Object.fromEntries(
              rateFields.map((key) => [
                key,
                (model[key as keyof Model] as number) / 1e6,
              ]),
            ),
            ...Object.fromEntries(
              imageRateFields.map((key) => [key, ((model[key as keyof Model] as number | undefined) ?? 0) / 1e6]),
            ),
            long_context_enabled: !!model.long_context,
            long_context_above: model.long_context?.above_input_tokens ?? "",
            ...Object.fromEntries(
              longContextRates.map(({ field, key }) => [field, model.long_context ? model.long_context[key] / 1e6 : ""]),
            ),
          }
        : {
            id: "",
            name: "",
            provider: "openai",
            upstream_model: "",
            enabled: true,
            billing_mode: "metered",
            manual_pricing: true,
            max_output_tokens: 4096,
            ...Object.fromEntries(rateFields.map((key) => [key, ""])),
            ...Object.fromEntries(imageRateFields.map((key) => [key, ""])),
            long_context_enabled: false,
            long_context_above: "",
            ...Object.fromEntries(longContextRates.map(({ field }) => [field, ""])),
          },
      onSubmit: async (values) => {
        if (model && values.id !== model.id)
          throw new Error(t.modelID + ": " + model.id);
        const id = String(values.id).trim();
        if (!model) {
          const latest = await api<Snapshot>("/api/state");
          if (latest.models.some((existing) => existing.id === id))
            throw new Error(t.modelIDExists);
        }
        await save({
          ...model,
          ...Object.fromEntries(Object.entries(values).filter(([key]) => !longContextFormKeys.includes(key))),
          billing_mode: "metered",
          manual_pricing: !!values.manual_pricing,
          ...(values.manual_pricing ? { pricing_reference: null, pricing_note: "" } : {}),
          id,
          ...(!model ? { create_only: true } : {}),
          ...Object.fromEntries(
            rateFields.map((key) => [
              key,
              dollars(values[key]),
            ]),
          ),
          ...Object.fromEntries(
            imageRateFields.map((key) => [key, routesImages(values) ? dollars(values[key]) : 0]),
          ),
          long_context:
            !routesImages(values) && values.long_context_enabled
              ? {
                  above_input_tokens: Number(values.long_context_above),
                  ...Object.fromEntries(longContextRates.map(({ field, key }) => [key, dollars(values[field])])),
                }
              : null,
          max_output_tokens: Number(values.max_output_tokens),
        });
      },
    });
  }
  const models = state.models.filter(
    (model) =>
      (provider === "all" || model.provider === provider) &&
      (status === "all" || (status === "enabled" ? model.enabled : !model.enabled)) &&
      `${model.id} ${model.name}`.toLowerCase().includes(search.toLowerCase()),
  );
  const enabledCount = state.models.filter((model) => model.enabled).length;
  const referenceCount = state.models.filter((model) => !!model.pricing_reference).length;
  const missingPriceCount = state.models.filter((model) => !isPriced(model)).length;
  const hasFilters = !!search || provider !== "all" || status !== "all";
  return (
    <div className="rp-management-models">
      <PageHeader
        title={t.models}
        description={t.modelsDescription}
        action={
          operator ? (
            <div className="rp-management-actions">
              <Button variant="light" startContent={<Plus size={15} />} onPress={() => open()}>
                {t.addModel}
              </Button>
              <Button variant="bordered" startContent={<RefreshCw size={15} />} isDisabled={!state.models.length} onPress={() => setUpdatingPrices(true)}>{p.update}</Button>
              <Button color="primary" startContent={<ArrowDownToLine size={16} />} onPress={() => setImporting(true)}>
                {t.modelImportTitle}
              </Button>
            </div>
          ) : undefined
        }
      />
      {operator && <div className="rp-management-metrics" role="group" aria-label={p.catalog}>
        {[
          { label: p.enabledModels, value: enabledCount, total: state.models.length, tone: "active" },
          { label: p.referenceModels, value: referenceCount, total: null, tone: "reference" },
          { label: missingPriceCount ? p.needsPricing : p.customModels, value: missingPriceCount || state.models.filter((model) => !model.pricing_reference && isPriced(model)).length, total: null, tone: missingPriceCount ? "attention" : "custom" },
        ].map((metric) => (
          <div key={metric.label} className="rp-management-metric" data-tone={metric.tone}>
            <span className="rp-management-metric-label"><i aria-hidden="true" />{metric.label}</span>
            <strong>{count(metric.value)}{metric.total !== null && <span> / {count(metric.total)}</span>}</strong>
          </div>
        ))}
      </div>}
      <section className="rp-management-catalog" aria-label={p.catalog}>
      <div className="rp-management-catalog-heading">
        <div className="rp-management-tabs" role="group" aria-label={p.filterStatus}>
          {[
            { id: "all", label: p.allModels, count: state.models.length },
            { id: "enabled", label: t.enabled, count: enabledCount },
            { id: "disabled", label: p.inactiveModels, count: state.models.length - enabledCount },
          ].map((filter) => <button key={filter.id} type="button" aria-pressed={status === filter.id} onClick={() => setStatus(filter.id)}>{filter.label}<span>{count(filter.count)}</span></button>)}
        </div>
        <span className="rp-management-unit">{p.pricingBasis}</span>
      </div>
      <div className="rp-management-filters">
        <Input
          aria-label={t.search}
          placeholder={t.search}
          value={search}
          onValueChange={setSearch}
          startContent={<Search size={16} />}
          className="rp-management-search"
          variant="bordered"
          isClearable
          onClear={() => setSearch("")}
        />
        <Select
          aria-label={p.filterProvider}
          className="rp-management-provider-filter"
          variant="bordered"
          disallowEmptySelection
          selectedKeys={[provider]}
          onChange={(e) => setProvider(e.target.value)}
        >
          <SelectItem key="all">{t.all}</SelectItem>
          <SelectItem key="openai">{t.openai}</SelectItem>
          <SelectItem key="anthropic">{t.anthropic}</SelectItem>
        </Select>
        <span className="rp-management-result-count" role="status">{p.results.replace("{visible}", count(models.length)).replace("{total}", count(state.models.length))}</span>
      </div>
      <DataTable
        caption={t.models}
        columns={[
          t.model,
          t.provider,
          t.input,
          t.output,
          t.cached,
          t.cacheWrite,
          t.status,
          ...(operator ? [t.actions] : []),
        ]}
        rows={models.map((model) => [
          <div key="model" className="rp-management-model-name">
            <p className="font-medium">{model.name}</p>
            <p className="mt-1 font-mono text-xs text-default-500">
              {model.id}
            </p>
            {!isPriced(model) && (
              <Chip radius="sm" size="sm" variant="flat" className="mt-2">
                {p.legacy}
              </Chip>
            )}
            {model.kind === "image" && (
              <Chip radius="sm" size="sm" variant="flat" className="mt-2">
                {im.imageModel}
              </Chip>
            )}
            {operator && model.pricing_reference && safeReferenceURL(model.pricing_reference.source_url) && (
              <Link href={safeReferenceURL(model.pricing_reference.source_url)!} target="_blank" rel="noopener noreferrer" className="rp-management-source-link" title={p.updatedAt.replace("{time}", new Date(model.pricing_reference.checked_at).toLocaleString())}>
                {p.referenceShort}<ArrowUpRight size={11} />
              </Link>
            )}
          </div>,
          <span key="provider" className="rp-management-provider"><i aria-hidden="true" data-provider={model.provider}>{model.provider === "anthropic" ? "A" : "O"}</i>{model.provider === "anthropic" ? t.anthropic : t.openai}</span>,
          ...modelRateCells(model),
          <Status key="enabled" enabled={model.enabled} />,
          ...(operator
            ? [
                <div key="actions" className="flex">
                  <IconButton label={t.edit} onPress={() => open(model)}>
                    <Edit3 size={16} />
                  </IconButton>
                  <IconButton
                    label={model.enabled ? t.disable : t.enable}
                    isLoading={busy === model.id}
                    onPress={() => {
                      if (model.enabled) setDisable(model);
                      else {
                        setBusy(model.id);
                        void save({ ...model, enabled: true })
                          .catch((e) =>
                            addToast({
                              title: errorMessage(e),
                              color: "danger",
                            }),
                          )
                          .finally(() => setBusy(""));
                      }
                    }}
                  >
                    <Power size={16} />
                  </IconButton>
                </div>,
              ]
            : []),
        ])}
        empty={
          <Empty
            title={hasFilters ? p.catalogEmpty : t.noModels}
            body={
              hasFilters
                ? t.resetSearch
                : operator
                  ? t.modelsEmptySetup
                  : t.noModelsBody
            }
            action={
              hasFilters ? <Button variant="bordered" onPress={() => { setSearch(""); setProvider("all"); setStatus("all"); }}>{p.resetFilters}</Button> : operator ? (
                <Button color="primary" onPress={() => setImporting(true)}>
                  {t.modelImportTitle}
                </Button>
              ) : undefined
            }
          />
        }
      />
      <div className="rp-management-catalog-footer"><ShieldCheck size={14} /><p>{p.balanceHelp}</p></div>
      </section>
      {updatingPrices && <ModelPriceUpdate onClose={() => setUpdatingPrices(false)} onUpdated={() => refresh({ throwOnError: true })} />}
      {importing && (
        <ModelImport
          credentials={(state.credentials || []).filter((credential) => !isBackup(state.endpoints?.find((endpoint) => endpoint.id === credential.endpoint_id)))}
          endpoints={(state.endpoints || []).filter((endpoint) => !isBackup(endpoint))}
          models={state.models}
          onClose={() => setImporting(false)}
          onCredentialSaved={refresh}
          onImported={() => refresh({ throwOnError: true })}
        />
      )}
      {form && <ResourceForm {...form} onClose={() => setForm(null)} />}{" "}
      {disable && (
        <Confirm
          title={t.disabledConfirmTitle}
          body={`${disable.name}: ${t.disabledConfirmBody}`}
          action={t.disable}
          onClose={() => setDisable(null)}
          onConfirm={() => save({ ...disable, enabled: false })}
        />
      )}
    </div>
  );
}
export function Routing() {
  const { state, refresh } = useConsole();
  const [strategy, setStrategy] = useState(state.routing.strategy);
  const [rpm, setRPM] = useState(String(state.routing.tenant_rpm));
  const [concurrency, setConcurrency] = useState(
    String(state.routing.tenant_concurrency),
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return (
    <>
      <PageHeader title={t.routing} description={t.routingDescription} />
      <form
        noValidate
        className="rp-console-routing-form"
        onSubmit={async (e) => {
          e.preventDefault();
          setError("");
          if (
            !Number.isInteger(Number(rpm)) ||
            Number(rpm) < 1 ||
            !Number.isInteger(Number(concurrency)) ||
            Number(concurrency) < 1
          ) {
            setError(t.invalidNumber);
            return;
          }
          setBusy(true);
          try {
            await api("/api/admin/routing", {
              strategy,
              tenant_rpm: Number(rpm),
              tenant_concurrency: Number(concurrency),
            });
            await refresh();
            addToast({ title: t.routingSaved, color: "success" });
          } catch (e) {
            setError(errorMessage(e));
          } finally {
            setBusy(false);
          }
        }}
      >
        <div className="rp-console-routing-main">
          <section className="rp-console-routing-section">
            <div className="rp-console-setting-header">
              <span className="rp-console-setting-icon">
                <SlidersHorizontal size={20} strokeWidth={1.6} />
              </span>
              <div>
                <h2>{t.strategy}</h2>
                <p>{t.routingDescription}</p>
              </div>
            </div>
            <RadioGroup
              aria-label={t.strategy}
              value={strategy}
              onValueChange={setStrategy}
              classNames={{ wrapper: "rp-console-strategy-options" }}
            >
              {[
                {
                  value: "least_load",
                  title: t.leastLoad,
                  body: t.leastLoadBody,
                },
                {
                  value: "highest_balance",
                  title: t.highestBalance,
                  body: t.highestBalanceBody,
                },
                { value: "balanced", title: t.balanced, body: t.balancedBody },
              ].map((option) => (
                <Radio
                  key={option.value}
                  value={option.value}
                  description={option.body}
                  classNames={{
                    base: "rp-console-strategy-option",
                    label: "text-sm font-medium",
                    description: "text-xs leading-6",
                    labelWrapper: "ml-2",
                  }}
                >
                  {option.title}
                </Radio>
              ))}
            </RadioGroup>
          </section>
          <section className="rp-console-routing-section rp-console-limits-section">
            <div className="rp-console-setting-header">
              <span className="rp-console-setting-icon">
                <GitBranch size={20} strokeWidth={1.6} />
              </span>
              <div>
                <h2>{t.tenantLimits}</h2>
                <p>{c.limitsBody}</p>
              </div>
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              <Input
                label={t.rpm}
                type="number"
                min={1}
                value={rpm}
                onValueChange={setRPM}
              />
              <Input
                label={t.concurrency}
                type="number"
                min={1}
                value={concurrency}
                onValueChange={setConcurrency}
              />
            </div>
          </section>
        </div>
        <aside className="rp-console-routing-explainer">
          <p className="rp-console-eyebrow">{c.routingEyebrow}</p>
          <h2>{c.routingPipeline}</h2>
          <p className="rp-console-panel-description">
            {c.routingPipelineBody}
          </p>
          <div className="rp-console-routing-flow">
            {[
              {
                title: c.routingEligibility,
                body: c.routingEligibilityBody,
                icon: Check,
              },
              {
                title: c.routingSelect,
                body: c.routingSelectBody,
                icon: GitBranch,
              },
              {
                title: c.routingDeliver,
                body: c.routingDeliverBody,
                icon: ArrowUpRight,
              },
            ].map((step, index) => (
              <div key={step.title}>
                <div className="rp-console-routing-flow-step">
                  <span>
                    <step.icon size={17} strokeWidth={1.6} />
                  </span>
                  <div>
                    <h3>{step.title}</h3>
                    <p>{step.body}</p>
                  </div>
                </div>
                {index < 2 && (
                  <ArrowDown
                    className="rp-console-routing-flow-arrow"
                    size={15}
                  />
                )}
              </div>
            ))}
          </div>
          <div className="rp-console-routing-note">
            <Network size={16} strokeWidth={1.6} />
            <p>{t.routingNote}</p>
          </div>
          <div className="rp-console-routing-note mt-4">
            <LifeBuoy size={16} strokeWidth={1.6} />
            <p>{b.routingBody}</p>
          </div>
        </aside>
        <div className="rp-console-routing-submit">
          {error && <ErrorBox message={error} />}
          <Button type="submit" color="primary" isLoading={busy}>
            {t.save}
          </Button>
        </div>
      </form>
    </>
  );
}
