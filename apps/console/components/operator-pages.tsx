"use client";
import { useState } from "react";
import Link from "next/link";
import {
  Button,
  Input,
  Select,
  SelectItem,
  RadioGroup,
  Radio,
  Chip,
  addToast,
} from "@heroui/react";
import {
  ArrowDown,
  ArrowUpRight,
  ArrowDownToLine,
  Check,
  Edit3,
  GitBranch,
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
import { api, count, dollars, errorMessage, money } from "@/lib/api";
import type { Credential, Endpoint, Model, Proxy, Snapshot } from "@/lib/types";
import { useConsole } from "./console-provider";
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
  const needsModels = (state.credentials || []).some((credential) => {
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
          { label: c.hostCount, value: state.endpoints?.length ?? 0 },
          { label: c.credentialCount, value: state.credentials?.length ?? 0 },
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
        <div className="mb-6 flex flex-wrap items-center justify-between gap-4 rounded-xl border border-default-200 p-5">
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
      <div className="rp-management-section-heading"><h2>{t.hosts}<span>{count(state.endpoints?.length ?? 0)}</span></h2></div>
      <DataTable
        caption={t.hosts}
        columns={[t.name, t.provider, t.baseURL, t.status, t.actions]}
        rows={(state.endpoints ?? []).map((endpoint) => [
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
        <h2>{t.credentials}<span>{count(state.credentials?.length ?? 0)}</span></h2>
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
          t.rpm,
          t.proxy,
          t.status,
          t.actions,
        ]}
        rows={(state.credentials ?? []).map((credential) => [
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
          count(credential.rpm),
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
      {credentialDialog && (
        <CredentialForm
          credential={credentialDialog.credential}
          endpoints={state.endpoints || []}
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
      ["input_rate", "output_rate", "cost_input_rate", "cost_output_rate"].some(
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
              [
                "input_rate",
                "output_rate",
                "cost_input_rate",
                "cost_output_rate",
              ].includes(key) &&
              (!Number.isFinite(Number(value)) || dollars(value) <= 0)
                ? t.modelPositiveRate
                : undefined,
            section: i === 0 ? t.salesRates : i === 4 ? t.costRates : undefined,
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
          ...values,
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
  const missingPriceCount = state.models.filter((model) => model.billing_mode === "usage_only" || !model.input_rate || !model.output_rate).length;
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
          { label: missingPriceCount ? p.needsPricing : p.customModels, value: missingPriceCount || state.models.filter((model) => !model.pricing_reference && model.input_rate > 0 && model.output_rate > 0 && model.billing_mode !== "usage_only").length, total: null, tone: missingPriceCount ? "attention" : "custom" },
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
            {(model.billing_mode === "usage_only" || !model.input_rate || !model.output_rate) && (
              <Chip size="sm" variant="flat" className="mt-2">
                {p.legacy}
              </Chip>
            )}
            {operator && model.pricing_reference && safeReferenceURL(model.pricing_reference.source_url) && (
              <Link href={safeReferenceURL(model.pricing_reference.source_url)!} target="_blank" rel="noopener noreferrer" className="rp-management-source-link" title={p.updatedAt.replace("{time}", new Date(model.pricing_reference.checked_at).toLocaleString())}>
                {p.referenceShort}<ArrowUpRight size={11} />
              </Link>
            )}
          </div>,
          <span key="provider" className="rp-management-provider"><i aria-hidden="true" data-provider={model.provider}>{model.provider === "anthropic" ? "A" : "O"}</i>{model.provider === "anthropic" ? t.anthropic : t.openai}</span>,
          ...[
            model.input_rate,
            model.output_rate,
            model.cached_rate,
            model.cache_write_rate,
          ].map((rate, i) => (
            <span key={i} className="rp-management-rate">
              {model.billing_mode === "usage_only" || !model.input_rate || !model.output_rate ? t.notPriced : money(rate, true)}
            </span>
          )),
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
          credentials={state.credentials || []}
          endpoints={state.endpoints || []}
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
