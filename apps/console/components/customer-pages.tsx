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
import { Integration } from "./marketing";
export function RequestTable({
  requests,
  short = false,
}: {
  requests: RequestLog[];
  short?: boolean;
}) {
  const [detail, setDetail] = useState<RequestLog | null>(null);
  const { state } = useConsole();
  return (
    <>
      <DataTable
        caption={t.requests}
        columns={[
          t.model,
          ...(state.user.operator && !short ? [t.tenant] : []),
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
            <p className="mt-1 text-[11px] text-default-400">
              {row.id.slice(0, 14)}
            </p>
          </div>,
          ...(state.user.operator && !short
            ? [
                state.customers?.find(
                  (customer) => customer.id === row.tenant_id,
                )?.name || row.tenant_id,
              ]
            : []),
          <Chip
            key="status"
            size="sm"
            variant="flat"
            color={
              row.status === "completed" || row.status === "succeeded"
                ? "success"
                : row.error_code
                  ? "danger"
                  : "default"
            }
          >
            {row.status}
          </Chip>,
          <span key="tokens" className="tabular-nums">
            {count(row.usage.input_tokens + row.usage.output_tokens)}
          </span>,
          <span key="charge" className="font-mono text-xs">
            {money(row.charge_micros, true)}
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
        <Modal
          isOpen
          onClose={() => setDetail(null)}
          size="lg"
          scrollBehavior="inside"
          classNames={{ base: "max-h-[85dvh]" }}
        >
          <ModalContent>
            <ModalHeader>{t.requestDetail}</ModalHeader>
            <ModalBody className="pb-6">
              <dl className="space-y-4">
                {[
                  [t.requestID, detail.id],
                  [t.customerID, detail.tenant_id],
                  [t.model, detail.model],
                  [t.provider, detail.provider],
                  [t.status, detail.status],
                  [t.inputTokens, count(detail.usage.input_tokens)],
                  [t.outputTokens, count(detail.usage.output_tokens)],
                  [t.cachedTokens, count(detail.usage.cached_tokens)],
                  [t.cacheWriteTokens, count(detail.usage.cache_write_tokens)],
                  [t.usageKnown, detail.usage.known ? t.yes : t.no],
                  [t.charge, money(detail.charge_micros, true)],
                  [t.held, money(detail.reserved_micros, true)],
                  [t.latency, `${count(detail.latency_ms)} ms`],
                  [t.key, detail.access_key_id],
                  [t.credential, detail.credential_id],
                  [t.errorCode, detail.error_code],
                ]
                  .filter(([, value]) => value !== undefined && value !== "")
                  .map(([label, value]) => (
                    <div
                      key={label}
                      className="flex justify-between gap-4 text-sm"
                    >
                      <dt className="shrink-0 text-default-500">{label}</dt>
                      <dd className="break-all text-right font-mono text-xs">
                        {value}
                      </dd>
                    </div>
                  ))}
              </dl>
            </ModalBody>
          </ModalContent>
        </Modal>
      )}
    </>
  );
}
export function Overview() {
  const { state } = useConsole();
  const sum = state.requests.reduce((n, r) => n + r.charge_micros, 0);
  const active = state.keys.filter((key) => key.enabled).length;
  const available = state.wallet.balance_micros - state.wallet.held_micros;
  const [docs, setDocs] = useState(false);
  const steps = [
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
      done: state.models.some((model) => model.enabled),
      href: "/app/models",
    },
  ];
  const completedSteps = steps.filter((step) => step.done).length;
  const metrics = [
    {
      label: t.balance,
      value: money(available),
      help: t.walletHelp,
      icon: Wallet,
    },
    {
      label: t.spend,
      value: money(sum, true),
      help: t.spendHelp,
      icon: ReceiptText,
    },
    {
      label: t.requests,
      value: count(state.requests.length),
      help: t.requestsHelp,
      icon: Activity,
    },
    {
      label: t.keyCount,
      value: count(active),
      help: state.project.name,
      icon: KeyRound,
    },
  ];
  return (
    <>
      <PageHeader
        title={t.overview}
        description={t.overviewDescription}
        action={
          <Button color="primary" as={Link} href="/app/keys">
            {t.createKey}
          </Button>
        }
      />
      <div className="rp-console-metrics">
        {metrics.map((metric) => (
          <div className="rp-console-metric" key={metric.label}>
            <div className="rp-console-metric-label">
              <span>{metric.label}</span>
              <metric.icon size={16} strokeWidth={1.6} />
            </div>
            <p className="rp-console-metric-value">{metric.value}</p>
            <p className="rp-console-metric-help">{metric.help}</p>
          </div>
        ))}
      </div>
      {completedSteps < steps.length && (
        <section className="rp-console-setup">
          <div className="rp-console-setup-intro">
            <p className="rp-console-eyebrow">{c.setupEyebrow}</p>
            <h2>{c.setupTitle}</h2>
            <p className="rp-console-setup-description">{c.setupBody}</p>
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
      <div className="rp-console-overview-grid">
        <section className="min-w-0">
          <div className="rp-console-section-heading">
            <div>
              <h2>{t.recentRequests}</h2>
              <p>{c.recentActivity}</p>
            </div>
            <Button as={Link} href="/app/usage" variant="light" size="sm">
              {t.seeAll}
            </Button>
          </div>
          <RequestTable requests={state.requests.slice(0, 6)} short />
        </section>
        <aside className="rp-console-developer-panel">
          <div className="rp-console-resource-icon">
            <Code2 size={20} strokeWidth={1.6} />
          </div>
          <p className="rp-console-eyebrow">{c.developerResources}</p>
          <h2>{c.endpointTitle}</h2>
          <p className="rp-console-panel-description">{c.endpointBody}</p>
          <div className="rp-console-endpoint-list">
            {[
              { label: t.openai, value: "https://relaypine.com/openai/v1" },
              { label: t.anthropic, value: "https://relaypine.com/anthropic" },
            ].map((endpoint) => (
              <div className="rp-console-endpoint" key={endpoint.label}>
                <div>
                  <span>{endpoint.label}</span>
                  <CopyButton value={endpoint.value} />
                </div>
                <code>{endpoint.value}</code>
              </div>
            ))}
          </div>
          <Button
            className="w-full"
            variant="bordered"
            onPress={() => setDocs(true)}
            endContent={<ArrowRight size={15} />}
          >
            {t.integration}
          </Button>
          <div className="rp-console-model-count">
            <Waypoints size={15} strokeWidth={1.6} />
            <span>{c.availableModels}</span>
            <strong>
              {count(state.models.filter((model) => model.enabled).length)}
            </strong>
          </div>
        </aside>
      </div>
      {docs && <Integration onClose={() => setDocs(false)} />}
    </>
  );
}
export function Keys() {
  const { state, refresh } = useConsole();
  const [adding, setAdding] = useState(false);
  const [secret, setSecret] = useState("");
  const [revoke, setRevoke] = useState<AccessKey | null>(null);
  const [docs, setDocs] = useState(false);
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
          <Button color="primary" onPress={() => setAdding(true)}>
            {t.createKey}
          </Button>
        }
      />
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
              <Button color="primary" onPress={() => setAdding(true)}>
                {t.createKey}
              </Button>
            }
          />
        }
      />
      <section className="mt-8">
        <div className="mb-4 flex items-center justify-between">
          <h2 className="font-medium">{t.integrationBase}</h2>
          <Button variant="light" size="sm" onPress={() => setDocs(true)}>
            {t.integration}
          </Button>
        </div>
        <div className="grid gap-4 lg:grid-cols-2">
          <CodeBlock label={t.openai} code="https://relaypine.com/openai/v1" />
          <CodeBlock
            label={t.anthropic}
            code="https://relaypine.com/anthropic"
          />
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
          size="lg"
          scrollBehavior="inside"
        >
          <ModalContent>
            <ModalHeader>{t.secretTitle}</ModalHeader>
            <ModalBody>
              <p className="text-sm leading-6 text-default-500">
                {t.secretBody}
              </p>
              <div className="mt-3 flex items-center gap-3 rounded-lg border border-default-200 p-3">
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
      {docs && <Integration onClose={() => setDocs(false)} />}
    </>
  );
}
export function Usage() {
  const { state } = useConsole();
  const [search, setSearch] = useState("");
  const rows = state.requests.filter((row) =>
    `${row.model} ${row.id} ${row.status} ${row.error_code ?? ""}`
      .toLowerCase()
      .includes(search.toLowerCase()),
  );
  return (
    <>
      <PageHeader title={t.usage} description={t.usageDescription} />
      <Input
        aria-label={t.searchRequests}
        placeholder={t.searchRequests}
        value={search}
        onValueChange={setSearch}
        isClearable
        onClear={() => setSearch("")}
        startContent={<Search size={17} className="text-default-400" />}
        className="rp-console-search max-w-md"
      />
      {search && !rows.length ? (
        <Empty
          title={t.noResults}
          body={t.resetSearch}
          action={
            <Button variant="flat" onPress={() => setSearch("")}>
              {t.all}
            </Button>
          }
        />
      ) : (
        <RequestTable requests={rows} />
      )}
      <p className="mt-3 text-xs text-default-400">
        {count(rows.length)} {t.totalRecords}
      </p>
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
          <p className="rp-console-wallet-label">{t.balance}</p>
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
          <h2>{w.internalTitle}</h2>
          <p>{w.internalBody}</p>
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
      <h2 className="rp-console-table-title">{t.ledger}</h2>
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
          <Chip key="status" size="sm" variant="flat">
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
    `${c.name} ${c.id}`.toLowerCase().includes(search.toLowerCase()),
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
        columns={[t.name, t.customerID, t.status, t.createdAt, t.actions]}
        rows={customers.map((customer) => [
          customer.name,
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
                name: customer.name,
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
