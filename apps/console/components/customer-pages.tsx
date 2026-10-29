"use client";
import { useState } from "react";
import Link from "next/link";
import {
  Button,
  Input,
  Modal,
  ModalContent,
  ModalHeader,
  ModalBody,
  ModalFooter,
  Chip,
  addToast,
} from "@heroui/react";
import {
  Activity,
  ArrowRight,
  ArrowUpRight,
  Check,
  Code2,
  ReceiptText,
  Eye,
  KeyRound,
  Plus,
  Search,
  Trash2,
  Wallet,
  Waypoints,
} from "lucide-react";
import { en as t } from "@/i18n/en";
import { consoleEn as c } from "@/i18n/console-en";
import { waitlistEn as w } from "@/i18n/waitlist-en";
import { api, count, date, dollars, money } from "@/lib/api";
import type { AccessKey, RequestLog } from "@/lib/types";
import { useConsole } from "./console-provider";
import {
  PageHeader,
  DataTable,
  Empty,
  IconButton,
  Status,
  Confirm,
  CopyButton,
  CodeBlock,
} from "./shared";
import { ResourceForm, type Field } from "./resource-form";
import {
  Integration,
  modelCount,
  sdkBaseURL,
  sdkLabel,
  sdkModels,
  sdkOrder,
} from "./marketing";
import { RequestDetailModal, RequestStatus, requestCharge } from "./request-detail";
import { RouteHint } from "./route-trail";
import { totalTokens as requestTokens } from "@/lib/images";
export function RequestTable({
  requests,
  short = false,
}: {
  requests: RequestLog[];
  short?: boolean;
}) {
  const [detail, setDetail] = useState<RequestLog | null>(null);
  const { state } = useConsole();
  const isOperator = state.surface === "admin" && state.user.operator;
  return (
    <>
      <DataTable
        caption={t.requests}
        columns={[
          t.model,
          ...(isOperator && !short ? [t.tenant] : []),
          t.status,
          t.tokens,
          t.charge,
          ...(short ? [] : [t.latency]),
          t.date,
          t.actions,
        ]}
        rows={requests.map((row) => [
          <div key="model">
            <p className="font-mono text-xs">{row.model}</p>
            <p className="mt-1 text-xs text-default-400">
              {row.id.slice(0, 14)}
            </p>
            {row.billing_mode === "usage_only" && (
              <p className="mt-1 text-xs text-default-500">
                {t.modelUsageOnly}
              </p>
            )}
            <RouteHint request={row} />
          </div>,
          ...(isOperator && !short
            ? [
                state.customers?.find(
                  (customer) => customer.id === row.tenant_id,
                )?.name || row.tenant_id,
              ]
            : []),
          <RequestStatus key="status" request={row} />,
          <span key="tokens" className="tabular-nums">
            {row.usage.known
              ? count(requestTokens(row.usage))
              : c.unknownUsage}
          </span>,
          <span key="charge" className="font-mono text-xs">
            {requestCharge(row)}
          </span>,
          ...(short
            ? []
            : [
                <span key="latency" className="tabular-nums">
                  {count(row.latency_ms)} ms
                </span>,
              ]),
          date(row.created_at),
          <IconButton
            key="details"
            label={t.details}
            onPress={() => setDetail(row)}
          >
            <Eye size={16} />
          </IconButton>,
        ])}
        empty={
          <Empty
            title={t.noRequests}
            body={t.noRequestsBody}
            action={
              <Button as={Link} href="/app/keys" variant="flat">
                {t.createKey}
              </Button>
            }
          />
        }
      />
      {detail && (
        <RequestDetailModal
          key={detail.id}
          request={detail}
          onClose={() => setDetail(null)}
        />
      )}
    </>
  );
}
export function Overview() {
  const { state } = useConsole();
  const isOperator = state.surface === "admin" && state.user.operator;
  const sum = state.requests.reduce((n, r) => n + r.charge_micros, 0);
  const activeKeys = state.keys.filter((key) => key.enabled).length;
  const available = state.wallet.balance_micros - state.wallet.held_micros;
  const enabledModels = state.models.filter((model) => model.enabled).length;
  const byApi = sdkModels(state.models);
  const [docs, setDocs] = useState(false);
  const baseURL = state.app_url.replace(/\/$/, "");
  const steps = isOperator
    ? [
        {
          title: c.hostStep,
          body: c.hostStepBody,
          done: !!state.endpoints?.some((host) => host.enabled),
          href: "/app/upstreams",
        },
        {
          title: c.credentialStep,
          body: c.credentialStepBody,
          done: !!state.credentials?.some((credential) => credential.enabled),
          href: "/app/upstreams",
        },
        {
          title: c.modelStep,
          body: c.modelStepBody,
          done: enabledModels > 0,
          href: "/app/models",
        },
      ]
    : [
        {
          title: c.keyStep,
          body: c.keyStepBody,
          done: activeKeys > 0,
          href: "/app/keys",
        },
        {
          title: c.balanceStep,
          body: c.balanceStepBody,
          done: available > 0,
          href: "/app/billing",
        },
        {
          title: c.requestStep,
          body: c.requestStepBody,
          done: state.requests.length > 0,
          href: "/app/usage",
        },
      ];
  const completedSteps = steps.filter((step) => step.done).length;
  const recent = state.requests.slice(0, 28).reverse();
  const peakTokens = Math.max(
    1,
    ...recent
      .filter((request) => request.usage.known)
      .map((request) => requestTokens(request.usage)),
  );
  const totalTokens = state.requests.reduce(
    (n, request) =>
      n +
      (request.usage.known ? requestTokens(request.usage) : 0),
    0,
  );
  const unknownUsage = state.requests.filter(
    (request) => !request.usage.known,
  ).length;
  const completed = state.requests.filter(
    (request) =>
      request.status === "completed" || request.status === "succeeded",
  ).length;
  const errors = state.requests.filter(
    (request) => !!request.error_code,
  ).length;
  const metrics = [
    {
      label: t.balance,
      value: money(available),
      help: c.availableToSpend,
      icon: Wallet,
      href: "/app/billing",
    },
    {
      label: c.recentSpend,
      value: money(sum, true),
      help: c.loadedRequests,
      icon: ReceiptText,
      href: "/app/billing",
    },
    {
      label: t.requests,
      value: count(state.requests.length),
      help: c.loadedRequests,
      icon: Activity,
      href: "/app/usage",
    },
    {
      label: c.activeKeys,
      value: count(activeKeys),
      help: state.project.name,
      icon: KeyRound,
      href: "/app/keys",
    },
  ];
  return (
    <>
      <PageHeader
        title={t.overview}
        description={
          isOperator ? c.operatorOverviewBody : c.customerOverviewBody
        }
        action={
          <div className="rp-console-header-actions">
            <Button
              variant="bordered"
              onPress={() => setDocs(true)}
              startContent={<Code2 size={16} />}
            >
              {t.integration}
            </Button>
            <Button
              color="primary"
              as={Link}
              href="/app/keys"
              startContent={<Plus size={16} />}
            >
              {t.createKey}
            </Button>
          </div>
        }
      />
      <div className="rp-console-metrics">
        {metrics.map((metric) => (
          <Link
            href={metric.href}
            className="rp-console-metric"
            key={metric.label}
          >
            <div className="rp-console-metric-label">
              <span>{metric.label}</span>
              <metric.icon size={16} strokeWidth={1.6} />
            </div>
            <p className="rp-console-metric-value">{metric.value}</p>
            <p className="rp-console-metric-help">
              {metric.help}
              <ArrowUpRight size={14} />
            </p>
          </Link>
        ))}
      </div>
      <div className="rp-console-observation-grid">
        <section
          className="rp-console-activity-panel"
          aria-labelledby="request-activity-title"
        >
          <div className="rp-console-activity-heading">
            <div>
              <p className="rp-console-eyebrow">{c.trafficEyebrow}</p>
              <h2 id="request-activity-title">{c.requestActivity}</h2>
            </div>
            <span className="rp-console-data-label">
              {c.latestRequests.replace("{count}", String(recent.length))}
            </span>
          </div>
          <div className="rp-console-activity-totals">
            <strong>{count(totalTokens)}</strong>
            <span>{c.tokensInView}</span>
          </div>
          {recent.length ? (
            <>
              <div
                className="rp-console-activity-chart"
                role="img"
                aria-label={c.chartDescription.replace(
                  "{count}",
                  String(recent.length),
                )}
              >
                <div className="rp-console-chart-grid" aria-hidden="true">
                  <span />
                  <span />
                  <span />
                </div>
                <div className="rp-console-chart-bars">
                  {recent.map((request) => {
                    const tokens = requestTokens(request.usage);
                    return (
                      <div
                        key={request.id}
                        className={`rp-console-chart-column ${request.error_code ? "has-error" : ""} ${!request.usage.known ? "is-unknown" : ""}`}
                        title={`${request.model} · ${request.usage.known ? `${count(tokens)} ${t.tokens}` : c.unknownUsage} · ${request.status}`}
                      >
                        <span
                          style={{
                            height: request.usage.known
                              ? `${Math.max(3, (tokens / peakTokens) * 100)}%`
                              : "7px",
                          }}
                        />
                      </div>
                    );
                  })}
                </div>
              </div>
              <div className="rp-console-chart-axis">
                <span>{c.earlierRequests}</span>
                <span>{c.latestRequest}</span>
              </div>
            </>
          ) : (
            <div className="rp-console-chart-empty">
              <Activity size={25} strokeWidth={1.25} />
              <p>{c.noActivityYet}</p>
              <span>{c.noActivityBody}</span>
            </div>
          )}
          <div className="rp-console-activity-footer">
            <span>
              <i />
              {count(completed)} {c.completedRequests}
            </span>
            <span>
              <i className="is-error" />
              {count(errors)} {c.failedRequests}
            </span>
            {unknownUsage > 0 && (
              <span>
                <i className="is-unknown" />
                {count(unknownUsage)} {c.unknownUsage}
              </span>
            )}
            <Link href="/app/usage">
              {t.seeAll}
              <ArrowRight size={14} />
            </Link>
          </div>
        </section>
        <aside className="rp-console-developer-panel">
          <div className="rp-console-panel-topline">
            <p className="rp-console-eyebrow">{c.developerResources}</p>
            <Code2 size={19} strokeWidth={1.6} />
          </div>
          <h2>{c.endpointTitle}</h2>
          <p className="rp-console-panel-description">{c.endpointBody}</p>
          <div className="rp-console-endpoint-list">
            {sdkOrder(byApi).map((provider) => (
              <div className="rp-console-endpoint" key={provider}>
                <div>
                  <span>
                    {sdkLabel(provider)}
                    <small>{modelCount(byApi[provider].length)}</small>
                  </span>
                  <CopyButton value={sdkBaseURL(baseURL, provider)} />
                </div>
                <code>{sdkBaseURL(baseURL, provider)}</code>
              </div>
            ))}
          </div>
          <Link className="rp-console-model-count" href="/app/models">
            <Waypoints size={15} strokeWidth={1.6} />
            <span>{c.availableModels}</span>
            <strong>{count(enabledModels)}</strong>
            <ArrowRight size={14} />
          </Link>
        </aside>
      </div>
      {completedSteps < steps.length && (
        <section className="rp-console-setup">
          <div className="rp-console-setup-intro">
            <p className="rp-console-eyebrow">{c.setupEyebrow}</p>
            <h2>{c.setupTitle}</h2>
            <p className="rp-console-setup-description">
              {isOperator ? c.setupBody : c.customerSetupBody}
            </p>
            <div className="rp-console-setup-progress">
              <div
                className="rp-console-setup-progress-track"
                aria-hidden="true"
              >
                {steps.map((step) => (
                  <span
                    key={step.title}
                    className={step.done ? "is-complete" : ""}
                  />
                ))}
              </div>
              <span>
                {completedSteps} / {steps.length} {c.setupProgress}
              </span>
            </div>
          </div>
          <div className="rp-console-setup-steps">
            {steps.map((step, index) => (
              <Link
                key={step.title}
                href={step.href}
                className={`rp-console-setup-step ${step.done ? "is-complete" : ""}`}
              >
                <span
                  className="rp-console-step-index"
                  aria-label={step.done ? c.complete : c.pending}
                >
                  {step.done ? (
                    <Check size={15} />
                  ) : (
                    String(index + 1).padStart(2, "0")
                  )}
                </span>
                <div>
                  <p>{step.title}</p>
                  <span>{step.body}</span>
                </div>
                <ArrowRight size={16} strokeWidth={1.6} />
              </Link>
            ))}
          </div>
        </section>
      )}
      <section className="rp-console-recent-section">
        <div className="rp-console-section-heading">
          <div>
            <p className="rp-console-eyebrow">{c.requestLogEyebrow}</p>
            <h2>{t.recentRequests}</h2>
          </div>
          <Button
            as={Link}
            href="/app/usage"
            variant="bordered"
            size="sm"
            endContent={<ArrowRight size={14} />}
          >
            {t.seeAll}
          </Button>
        </div>
        <RequestTable requests={state.requests.slice(0, 6)} short />
      </section>
      {docs && (
        <Integration models={state.models} onClose={() => setDocs(false)} />
      )}
    </>
  );
}
export function Keys() {
  const { state, refresh } = useConsole();
  const [adding, setAdding] = useState(false);
  const [secret, setSecret] = useState("");
  const [revoke, setRevoke] = useState<AccessKey | null>(null);
  const [docs, setDocs] = useState(false);
  const baseURL = state.app_url.replace(/\/$/, "");
  const byApi = sdkModels(state.models);
  const fields: Field[] = [
    {
      key: "name",
      label: t.name,
      placeholder: t.keyNamePlaceholder,
      required: true,
    },
    { key: "rpm", label: t.rpm, type: "number", min: 1, required: true },
    { key: "models", label: t.allowedModels, description: t.modelAccessHelp },
  ];
  return (
    <>
      <PageHeader
        title={t.keys}
        description={t.keysDescription}
        action={
          <Button
            color="primary"
            startContent={<Plus size={16} />}
            onPress={() => setAdding(true)}
          >
            {t.createKey}
          </Button>
        }
      />
      <div className="rp-console-resource-summary">
        <div>
          <span>{c.keysConfigured}</span>
          <strong>{count(state.keys.length)}</strong>
        </div>
        <div>
          <span>{c.activeKeys}</span>
          <strong>
            {count(state.keys.filter((key) => key.enabled).length)}
          </strong>
        </div>
        <div>
          <span>{c.keyRevokedCount}</span>
          <strong>
            {count(state.keys.filter((key) => !key.enabled).length)}
          </strong>
        </div>
      </div>
      <DataTable
        caption={t.keys}
        columns={[
          t.name,
          t.prefix,
          t.model,
          t.rpm,
          t.status,
          t.createdAt,
          t.actions,
        ]}
        rows={state.keys.map((key) => [
          key.name,
          <code key="prefix" className="text-xs">
            {key.prefix}••••
          </code>,
          key.models.length ? key.models.join(", ") : t.allModels,
          count(key.rpm),
          <Status
            key="enabled"
            enabled={key.enabled}
            label={key.enabled ? t.active : t.revoked}
          />,
          date(key.created_at),
          key.enabled ? (
            <IconButton
              key="revoke"
              label={t.revoke}
              color="danger"
              onPress={() => setRevoke(key)}
            >
              <Trash2 size={16} />
            </IconButton>
          ) : (
            t.emptyValue
          ),
        ])}
        empty={
          <Empty
            title={t.noKeys}
            body={t.noKeysBody}
            action={
              <Button
                color="primary"
                startContent={<Plus size={16} />}
                onPress={() => setAdding(true)}
              >
                {t.createKey}
              </Button>
            }
          />
        }
      />
      <section className="rp-console-key-guide">
        <div className="rp-console-section-heading">
          <div>
            <p className="rp-console-eyebrow">{c.developerResources}</p>
            <h2>{t.integrationBase}</h2>
          </div>
          <Button variant="light" size="sm" onPress={() => setDocs(true)}>
            {t.integration}
          </Button>
        </div>
        <div className="grid gap-4 lg:grid-cols-2">
          {sdkOrder(byApi).map((provider) => (
            <CodeBlock
              key={provider}
              label={`${sdkLabel(provider)} · ${modelCount(byApi[provider].length)}`}
              code={sdkBaseURL(baseURL, provider)}
            />
          ))}
        </div>
      </section>
      {adding && (
        <ResourceForm
          title={t.createKey}
          fields={fields}
          initial={{ name: "", rpm: 60, models: "" }}
          submitLabel={t.createKey}
          onClose={() => setAdding(false)}
          onSubmit={async (values) => {
            const result = await api<{ secret: string }>("/api/keys", {
              name: values.name,
              rpm: Number(values.rpm),
              models: String(values.models)
                .split(",")
                .map((s) => s.trim())
                .filter(Boolean),
            });
            setSecret(result.secret);
            await refresh();
          }}
        />
      )}
      {secret && (
        <Modal
          isOpen
          onClose={() => setSecret("")}
          classNames={{ base: "rp-modal max-h-[85dvh]" }}
          size="lg"
          scrollBehavior="inside"
        >
          <ModalContent>
            <ModalHeader>{t.secretTitle}</ModalHeader>
            <ModalBody>
              <p className="text-sm leading-6 text-default-500">
                {t.secretBody}
              </p>
              <div className="mt-3 flex items-center gap-3 rounded-[3px] border border-default-200 p-3">
                <code
                  aria-label={t.keySecretLabel}
                  className="min-w-0 flex-1 break-all text-xs"
                >
                  {secret}
                </code>
                <CopyButton value={secret} />
              </div>
            </ModalBody>
            <ModalFooter>
              <Button color="primary" onPress={() => setSecret("")}>
                {t.close}
              </Button>
            </ModalFooter>
          </ModalContent>
        </Modal>
      )}
      {revoke && (
        <Confirm
          title={t.revokeTitle}
          body={`${revoke.name}: ${t.revokeBody}`}
          action={t.revoke}
          onClose={() => setRevoke(null)}
          onConfirm={async () => {
            await api(`/api/keys/${revoke.id}`, undefined, "DELETE");
            await refresh();
            addToast({ title: t.keyRevoked, color: "success" });
          }}
        />
      )}
      {docs && (
        <Integration models={state.models} onClose={() => setDocs(false)} />
      )}
    </>
  );
}
export function Usage() {
  const { state } = useConsole();
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState<"all" | "completed" | "errors">("all");
  const rows = state.requests.filter((row) => {
    const matchesSearch =
      `${row.model} ${row.id} ${row.status} ${row.error_code ?? ""}`
        .toLowerCase()
        .includes(search.toLowerCase());
    const matchesStatus =
      filter === "all" ||
      (filter === "completed"
        ? row.status === "completed" || row.status === "succeeded"
        : !!row.error_code);
    return matchesSearch && matchesStatus;
  });
  return (
    <>
      <PageHeader title={t.usage} description={t.usageDescription} />
      <div className="rp-console-usage-toolbar">
        <div className="rp-console-segmented" aria-label={t.status}>
          {(
            [
              { id: "all", label: c.allRequestsFilter },
              { id: "completed", label: c.successfulFilter },
              { id: "errors", label: c.errorsFilter },
            ] as const
          ).map((item) => (
            <Button
              key={item.id}
              variant="light"
              aria-pressed={filter === item.id}
              onPress={() => setFilter(item.id)}
            >
              {item.label}
            </Button>
          ))}
        </div>
        <Input
          aria-label={t.searchRequests}
          placeholder={t.searchRequests}
          value={search}
          onValueChange={setSearch}
          isClearable
          onClear={() => setSearch("")}
          startContent={<Search size={16} className="text-default-400" />}
          className="rp-console-search"
        />
      </div>
      {(search || filter !== "all") && !rows.length ? (
        <Empty
          title={t.noResults}
          body={t.resetSearch}
          action={
            <Button
              variant="flat"
              onPress={() => {
                setSearch("");
                setFilter("all");
              }}
            >
              {t.all}
            </Button>
          }
        />
      ) : (
        <RequestTable requests={rows} />
      )}
      <div className="rp-console-table-meta">
        <span>
          {count(rows.length)} {c.matchingRequests}
        </span>
        <span>{c.usageScopeNote}</span>
      </div>
    </>
  );
}
export function Billing() {
  const { state } = useConsole();
  return (
    <>
      <PageHeader title={t.billing} description={t.billingDescription} />
      <div className="rp-console-billing-summary">
        <section className="rp-console-wallet">
          <div className="rp-console-wallet-heading">
            <span className="rp-console-eyebrow">{c.billingEyebrow}</span>
            <span className="rp-console-currency">{c.currency}</span>
          </div>
          <p className="rp-console-wallet-label">{c.availableToSpend}</p>
          <p className="rp-console-wallet-value">
            {money(state.wallet.balance_micros - state.wallet.held_micros)}
          </p>
          <div className="rp-console-wallet-reserved">
            <div>
              <span>{t.held}</span>
              <strong>{money(state.wallet.held_micros)}</strong>
            </div>
            <p>{t.billingHold}</p>
          </div>
        </section>
        <section className="rp-console-accounting-note">
          <div className="rp-console-accounting-icon">
            <ReceiptText size={22} strokeWidth={1.5} />
          </div>
          <p className="rp-console-eyebrow">{w.internalLabel}</p>
          <h2>{c.balanceLedger}</h2>
          <p>{c.balanceLedgerBody}</p>
          <Button
            as={Link}
            href="/app/usage"
            variant="bordered"
            endContent={<ArrowRight size={15} />}
          >
            {t.usage}
          </Button>
        </section>
      </div>
      <div className="rp-console-section-heading">
        <div>
          <p className="rp-console-eyebrow">{w.internalLabel}</p>
          <h2>{t.ledger}</h2>
        </div>
        <span className="text-xs text-default-500">
          {count(state.ledger.length)} {t.totalRecords}
        </span>
      </div>
      <DataTable
        caption={t.ledger}
        columns={[t.entryType, t.amount, t.reason, t.reference, t.date]}
        rows={state.ledger.map((entry) => [
          entry.kind,
          <span key="amount" className="font-mono text-xs">
            {money(entry.amount_micros, true)}
          </span>,
          <span key="reason" className="inline-block max-w-xs truncate">
            {entry.reason || t.emptyValue}
          </span>,
          <code key="reference" className="text-xs">
            {entry.reference || t.emptyValue}
          </code>,
          date(entry.created_at),
        ])}
        empty={<Empty title={t.noLedger} body={t.noLedgerBody} />}
      />
    </>
  );
}
export function Waitlist() {
  const { state } = useConsole();
  const [search, setSearch] = useState("");
  const entries = (state.waitlist ?? []).filter((entry) =>
    `${entry.name} ${entry.email}`
      .toLowerCase()
      .includes(search.trim().toLowerCase()),
  );
  return (
    <>
      <PageHeader title={w.pageTitle} description={w.pageDescription} />
      <Input
        aria-label={w.search}
        placeholder={w.search}
        value={search}
        onValueChange={setSearch}
        isClearable
        onClear={() => setSearch("")}
        startContent={<Search size={17} className="text-default-400" />}
        className="rp-console-search max-w-md"
      />
      <DataTable
        caption={w.pageTitle}
        columns={[t.name, t.email, t.status, w.joined]}
        rows={entries.map((entry) => [
          <span key="name" className="font-medium">
            {entry.name}
          </span>,
          entry.email,
          <Chip radius="sm" key="status" size="sm" variant="flat">
            {w.status}
          </Chip>,
          date(entry.created_at),
        ])}
        empty={
          <Empty
            title={search ? w.noMatches : w.noEntries}
            body={search ? t.resetSearch : w.noEntriesBody}
            action={
              search ? (
                <Button variant="flat" onPress={() => setSearch("")}>
                  {t.all}
                </Button>
              ) : undefined
            }
          />
        }
      />
      <p className="mt-3 text-xs text-default-400">
        {w.entries}: {count(entries.length)}
      </p>
    </>
  );
}
export function Customers() {
  const { state, refresh } = useConsole();
  const [credit, setCredit] = useState<{
    id: string;
    name: string;
    idempotency: string;
  } | null>(null);
  const [search, setSearch] = useState("");
  const customers = (state.customers ?? []).filter((c) =>
    `${c.name} ${c.email || ""} ${c.id}`
      .toLowerCase()
      .includes(search.toLowerCase()),
  );
  return (
    <>
      <PageHeader title={t.customers} description={t.customersDescription} />
      <Input
        aria-label={t.search}
        placeholder={t.search}
        value={search}
        onValueChange={setSearch}
        startContent={<Search size={16} />}
        className="rp-console-search max-w-md"
      />
      <DataTable
        caption={t.customers}
        columns={[
          t.name,
          t.email,
          t.customerID,
          t.status,
          t.createdAt,
          t.actions,
        ]}
        rows={customers.map((customer) => [
          customer.name,
          customer.email || t.emptyValue,
          <code key="id" className="text-xs">
            {customer.id}
          </code>,
          customer.status,
          date(customer.created_at),
          <IconButton
            key="credit"
            label={t.credit}
            onPress={() =>
              setCredit({
                id: customer.id,
                name: customer.email
                  ? `${customer.name} (${customer.email})`
                  : customer.name,
                idempotency: crypto.randomUUID(),
              })
            }
          >
            <Plus size={16} />
          </IconButton>,
        ])}
        empty={
          <Empty
            title={search ? t.noResults : t.noCustomers}
            body={search ? t.resetSearch : t.noCustomersBody}
          />
        }
      />
      {credit && (
        <ResourceForm
          title={`${t.creditTitle}: ${credit.name}`}
          description={t.creditDescription}
          fields={[
            {
              key: "amount",
              label: t.creditAmount,
              type: "number",
              min: 0.01,
              required: true,
            },
            {
              key: "reason",
              label: t.creditReason,
              placeholder: t.creditReasonPlaceholder,
              required: true,
            },
          ]}
          initial={{ amount: 20, reason: "" }}
          submitLabel={t.credit}
          onClose={() => setCredit(null)}
          onSubmit={async (values) => {
            if (Number(values.amount) <= 0) throw new Error(t.amountPositive);
            await api("/api/admin/credits", {
              tenant_id: credit.id,
              amount_micros: dollars(values.amount),
              reason: String(values.reason).trim(),
              idempotency_key: credit.idempotency,
            });
            await refresh();
            addToast({ title: t.creditDone, color: "success" });
          }}
        />
      )}
    </>
  );
}
