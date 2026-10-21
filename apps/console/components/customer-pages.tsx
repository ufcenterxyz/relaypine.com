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
import { ArrowRight, Eye, Plus, Search, Trash2 } from "lucide-react";
import { en as t } from "@/i18n/en";
import { api, count, date, dollars, money } from "@/lib/api";
import type { AccessKey, Order, RequestLog } from "@/lib/types";
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
  const active = state.keys.filter((k) => k.enabled).length;
  const needsSetup =
    state.user.operator &&
    (!state.credentials?.length || !state.models.some((m) => m.enabled));
  const [docs, setDocs] = useState(false);
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
      <div className="mb-8 grid grid-cols-2 gap-3 lg:grid-cols-4">
        {[
          {
            label: t.balance,
            value: money(
              state.wallet.balance_micros - state.wallet.held_micros,
            ),
            help: t.walletHelp,
          },
          { label: t.spend, value: money(sum, true), help: t.spendHelp },
          {
            label: t.requests,
            value: count(state.requests.length),
            help: t.requestsHelp,
          },
          { label: t.keyCount, value: count(active), help: state.project.name },
        ].map((metric) => (
          <div
            className="rounded-xl border border-default-200 p-5"
            key={metric.label}
          >
            <p className="text-xs text-default-500">{metric.label}</p>
            <p className="mt-3 text-2xl font-semibold tabular-nums tracking-tight">
              {metric.value}
            </p>
            <p className="mt-3 text-[11px] leading-5 text-default-400">
              {metric.help}
            </p>
          </div>
        ))}
      </div>
      {(needsSetup || state.requests.length === 0) && (
        <div className="mb-8 flex flex-wrap items-center justify-between gap-6 rounded-xl border border-default-200 bg-default-50 p-6">
          <div>
            <h2 className="font-medium">
              {needsSetup ? t.setupOperator : t.readyTitle}
            </h2>
            <p className="mt-2 max-w-xl text-sm leading-6 text-default-500">
              {needsSetup ? t.setupOperatorBody : t.readyBody}
            </p>
          </div>
          <Button
            as={Link}
            href={needsSetup ? "/app/upstreams" : "/app/billing"}
            variant="bordered"
            endContent={<ArrowRight size={16} />}
          >
            {needsSetup ? t.addUpstream : t.topUp}
          </Button>
        </div>
      )}
      <div className="mb-4 flex items-center justify-between">
        <h2 className="font-medium">{t.recentRequests}</h2>
        <Button as={Link} href="/app/usage" variant="light" size="sm">
          {t.seeAll}
        </Button>
      </div>
      <RequestTable requests={state.requests.slice(0, 6)} short />
      <div className="mt-7 flex flex-wrap items-center justify-between gap-3 border-t border-default-100 pt-5">
        <span className="text-sm text-default-500">{t.nativeTitle}</span>
        <Button variant="light" size="sm" onPress={() => setDocs(true)}>
          {t.integration}
        </Button>
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
        className="mb-5 max-w-md"
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
  const { state, refresh } = useConsole();
  const [fund, setFund] = useState(false);
  const [refund, setRefund] = useState<Order | null>(null);
  const notice =
    typeof window !== "undefined"
      ? new URLSearchParams(window.location.search).get("checkout")
      : null;
  return (
    <>
      <PageHeader
        title={t.billing}
        description={t.billingDescription}
        action={
          <Button
            color="primary"
            isDisabled={!state.payments_enabled}
            onPress={() => setFund(true)}
          >
            {t.topUp}
          </Button>
        }
      />
      {notice && (
        <div className="mb-6 rounded-xl border border-default-200 bg-default-50 p-4 text-sm leading-6">
          {notice === "cancelled" ? t.paymentCancelled : t.paymentReturn}
        </div>
      )}
      <div className="mb-8 grid gap-4 md:grid-cols-[1fr_1.4fr]">
        <div className="rounded-xl border border-default-200 p-6">
          <p className="text-sm text-default-500">{t.balance}</p>
          <p className="mt-4 text-4xl font-semibold tabular-nums tracking-tight">
            {money(state.wallet.balance_micros - state.wallet.held_micros)}
          </p>
          <p className="mt-5 text-xs text-default-500">
            {t.held}: {money(state.wallet.held_micros)}
          </p>
          <p className="mt-2 text-xs leading-5 text-default-400">
            {t.billingHold}
          </p>
        </div>
        <div className="flex flex-col justify-center rounded-xl border border-default-200 p-6">
          <h2 className="font-medium">
            {state.payments_enabled ? t.topUpTitle : t.paymentUnavailable}
          </h2>
          <p className="mt-3 text-sm leading-7 text-default-500">
            {state.payments_enabled
              ? t.topUpDescription
              : t.paymentUnavailableBody}
          </p>
          {state.payments_enabled && (
            <Button
              className="mt-5 self-start"
              variant="bordered"
              onPress={() => setFund(true)}
            >
              {t.topUp}
            </Button>
          )}
        </div>
      </div>
      <h2 className="mb-4 font-medium">{t.payments}</h2>
      <DataTable
        caption={t.payments}
        columns={[
          t.orderID,
          ...(state.user.operator ? [t.tenant] : []),
          t.amount,
          t.status,
          t.date,
          ...(state.user.operator ? [t.actions] : []),
        ]}
        rows={state.orders.map((order) => [
          <code key="order" className="text-xs">
            {order.id}
          </code>,
          ...(state.user.operator
            ? [
                state.customers?.find(
                  (customer) => customer.id === order.tenant_id,
                )?.name || order.tenant_id,
              ]
            : []),
          money(order.amount_micros),
          <Chip
            key="status"
            size="sm"
            variant="flat"
            color={order.status === "paid" ? "success" : "default"}
          >
            {order.status}
          </Chip>,
          date(order.created_at),
          ...(state.user.operator
            ? [
                order.status === "paid" ? (
                  <Button
                    key="refund"
                    size="sm"
                    variant="light"
                    color="danger"
                    onPress={() => setRefund(order)}
                  >
                    {t.refund}
                  </Button>
                ) : (
                  t.emptyValue
                ),
              ]
            : []),
        ])}
        empty={<Empty title={t.noPayments} body={t.noPaymentsBody} />}
      />
      <h2 className="mb-4 mt-8 font-medium">{t.ledger}</h2>
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
      {fund && (
        <ResourceForm
          title={t.topUpTitle}
          description={t.topUpDescription}
          fields={[
            {
              key: "amount",
              label: t.topUpAmount,
              type: "number",
              min: 5,
              required: true,
            },
          ]}
          initial={{ amount: 20 }}
          submitLabel={t.checkout}
          onClose={() => setFund(false)}
          onSubmit={async (v) => {
            const amount = Number(v.amount);
            if (amount < 5 || amount > 1000) throw new Error(t.amountRange);
            const result = await api<{ url: string }>("/api/billing/checkout", {
              amount_cents: Math.round(amount * 100),
            });
            const url = new URL(result.url);
            if (
              url.protocol !== "https:" ||
              url.hostname !== "checkout.stripe.com"
            )
              throw new Error(t.externalRedirectError);
            window.location.assign(url.href);
          }}
        />
      )}
      {refund && (
        <Confirm
          title={t.refundTitle}
          body={`${money(refund.amount_micros)} · ${refund.id}. ${t.refundBody}`}
          action={t.refund}
          onClose={() => setRefund(null)}
          onConfirm={async () => {
            await api("/api/admin/refunds", { order_id: refund.id });
            await refresh();
            addToast({ title: t.refundDone, color: "success" });
          }}
        />
      )}
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
        className="mb-5 max-w-md"
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
