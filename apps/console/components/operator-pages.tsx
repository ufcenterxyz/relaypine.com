"use client";
import { useState } from "react";
import {
  Button,
  Input,
  Select,
  SelectItem,
  RadioGroup,
  Radio,
  addToast,
} from "@heroui/react";
import {
  ArrowDown,
  ArrowUpRight,
  Check,
  Edit3,
  GitBranch,
  Network,
  Power,
  Plus,
  Search,
  ShieldCheck,
  SlidersHorizontal,
} from "lucide-react";
import { en as t } from "@/i18n/en";
import { consoleEn as c } from "@/i18n/console-en";
import { api, count, dollars, errorMessage, money } from "@/lib/api";
import type { Credential, Endpoint, Model, Proxy } from "@/lib/types";
import { useConsole } from "./console-provider";
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
const numberFields = (values: FormValues, keys: string[]) =>
  Object.fromEntries(keys.map((key) => [key, Number(values[key] ?? 0)]));
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
  const [disable, setDisable] = useState<{
    name: string;
    run: () => Promise<void>;
  } | null>(null);
  const [busy, setBusy] = useState("");
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
    setForm({
      title: credential ? t.editCredential : t.addCredential,
      fields: [
        { key: "name", label: t.name, required: true },
        {
          key: "endpoint_id",
          label: t.endpoint,
          type: "select",
          required: true,
          options: (state.endpoints ?? []).map((e) => ({
            key: e.id,
            label: e.name,
          })),
        },
        {
          key: "secret",
          label: credential ? t.rotateSecret : t.secret,
          type: "password",
          required: !credential,
          description: credential ? t.preserveSecret : t.protectedSecret,
        },
        {
          key: "quota_group",
          label: t.quotaGroup,
          required: true,
          description: t.quotaHelp,
        },
        {
          key: "proxy_id",
          label: t.proxy,
          type: "select",
          options: [
            { key: "direct", label: t.direct },
            ...(state.proxies ?? []).map((p) => ({ key: p.id, label: p.name })),
          ],
        },
        { key: "rpm", label: t.rpm, type: "number", min: 1, required: true },
        { key: "tpm", label: t.tpm, type: "number", min: 1, required: true },
        {
          key: "max_concurrency",
          label: t.concurrency,
          type: "number",
          min: 1,
          required: true,
        },
        {
          key: "budget_micros",
          label: t.budget,
          type: "number",
          description: t.budgetHelp,
        },
        { key: "priority", label: t.priority, type: "number" },
        enabledField,
      ],
      initial: credential
        ? {
            id: credential.id,
            name: credential.name,
            endpoint_id: credential.endpoint_id,
            quota_group: credential.quota_group,
            proxy_id: credential.proxy_id || "direct",
            secret: "",
            rpm: credential.rpm,
            tpm: credential.tpm,
            max_concurrency: credential.max_concurrency,
            budget_micros: credential.budget_micros / 1e6,
            priority: credential.priority,
            enabled: credential.enabled,
          }
        : {
            name: "",
            endpoint_id: state.endpoints?.[0]?.id ?? "",
            secret: "",
            quota_group: "",
            proxy_id: "direct",
            rpm: 60,
            tpm: 100000,
            max_concurrency: 10,
            budget_micros: 0,
            priority: 0,
            enabled: true,
          },
      onSubmit: async (values) => {
        await save("credentials", {
          ...credential,
          ...values,
          proxy_id: values.proxy_id === "direct" ? "" : values.proxy_id,
          ...numberFields(values, [
            "rpm",
            "tpm",
            "max_concurrency",
            "priority",
          ]),
          budget_micros: dollars(values.budget_micros),
        });
      },
    });
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
          <Button color="primary" onPress={() => hostForm()}>
            {t.addHost}
          </Button>
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
      <h2 className="rp-console-table-title">{t.hosts}</h2>
      <DataTable
        caption={t.hosts}
        columns={[t.name, t.provider, t.baseURL, t.status, t.actions]}
        rows={(state.endpoints ?? []).map((endpoint) => [
          endpoint.name,
          endpoint.provider,
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
      <div className="rp-console-section-heading rp-console-section-heading-spaced">
        <h2 className="font-medium">{t.credentials}</h2>
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
          state.endpoints?.find((e) => e.id === credential.endpoint_id)?.name ||
            credential.endpoint_id,
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
  const [form, setForm] = useState<FormConfig | null>(null);
  const [disable, setDisable] = useState<Model | null>(null);
  const [busy, setBusy] = useState("");
  const [search, setSearch] = useState("");
  const [provider, setProvider] = useState("all");
  async function save(body: unknown) {
    await api("/api/admin/models", body);
    await refresh();
    addToast({ title: t.saved, color: "success" });
  }
  function open(model?: Model) {
    setForm({
      title: model ? t.editModel : t.addModel,
      description: t.ratesHelp,
      fields: [
        { key: "id", label: t.modelID, required: true, readOnly: !!model },
        { key: "name", label: t.name, required: true },
        {
          key: "provider",
          label: t.provider,
          type: "select",
          options: providers,
          required: true,
        },
        { key: "upstream_model", label: t.upstreamModel, required: true },
        ...rateFields.map(
          (key, i): Field => ({
            key,
            label: rateLabels[i],
            type: "number",
            required: true,
            section: i === 0 ? t.salesRates : i === 4 ? t.costRates : undefined,
          }),
        ),
        {
          key: "max_output_tokens",
          label: t.maxOutput,
          type: "number",
          min: 1,
          required: true,
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
            max_output_tokens: 4096,
            ...Object.fromEntries(rateFields.map((key) => [key, 0])),
          },
      onSubmit: async (values) => {
        if (model && values.id !== model.id)
          throw new Error(t.modelID + ": " + model.id);
        await save({
          ...model,
          ...values,
          ...Object.fromEntries(
            rateFields.map((key) => [key, dollars(values[key])]),
          ),
          max_output_tokens: Number(values.max_output_tokens),
        });
      },
    });
  }
  const models = state.models.filter(
    (model) =>
      (provider === "all" || model.provider === provider) &&
      `${model.id} ${model.name}`.toLowerCase().includes(search.toLowerCase()),
  );
  return (
    <>
      <PageHeader
        title={t.models}
        description={t.modelsDescription}
        action={
          state.user.operator ? (
            <Button color="primary" onPress={() => open()}>
              {t.addModel}
            </Button>
          ) : undefined
        }
      />
      <div className="rp-console-filter-row">
        <Input
          aria-label={t.search}
          placeholder={t.search}
          value={search}
          onValueChange={setSearch}
          startContent={<Search size={16} />}
          className="max-w-sm"
        />
        <Select
          aria-label={t.provider}
          className="max-w-40"
          selectedKeys={[provider]}
          onChange={(e) => setProvider(e.target.value)}
        >
          <SelectItem key="all">{t.all}</SelectItem>
          <SelectItem key="openai">{t.openai}</SelectItem>
          <SelectItem key="anthropic">{t.anthropic}</SelectItem>
        </Select>
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
          ...(state.user.operator ? [t.actions] : []),
        ]}
        rows={models.map((model) => [
          <div key="model">
            <p className="font-medium">{model.name}</p>
            <p className="mt-1 font-mono text-xs text-default-500">
              {model.id}
            </p>
          </div>,
          model.provider,
          ...[
            model.input_rate,
            model.output_rate,
            model.cached_rate,
            model.cache_write_rate,
          ].map((rate, i) => (
            <span key={i} className="font-mono text-xs">
              {money(rate, true)}
            </span>
          )),
          <Status key="enabled" enabled={model.enabled} />,
          ...(state.user.operator
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
            title={search ? t.noResults : t.noModels}
            body={search ? t.resetSearch : t.noModelsBody}
            action={
              state.user.operator ? (
                <Button color="primary" onPress={() => open()}>
                  {t.addModel}
                </Button>
              ) : undefined
            }
          />
        }
      />
      <p className="mt-3 text-xs text-default-400">{t.perMillion}</p>
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
    </>
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
