"use client";
import type { ReactNode } from "react";
import { useEffect, useMemo, useState } from "react";
import {
  Button,
  Chip,
  Modal,
  ModalBody,
  ModalContent,
  ModalHeader,
  Skeleton,
  Tab,
  Tabs,
} from "@heroui/react";
import { Activity, ImageOff, RefreshCw } from "lucide-react";
import { en as t } from "@/i18n/en";
import { consoleEn as c } from "@/i18n/console-en";
import { requestDetailEn as d } from "@/i18n/request-detail-en";
import { api, count, date, errorMessage, money } from "@/lib/api";
import {
  displayJSON,
  displayValue,
  formatBytes,
  requestConversation,
  responseOutput,
  type Conversation,
  type ImageSource,
  type Output,
  type Part,
  type Turn,
} from "@/lib/request-payload";
import type {
  CapturedResponse,
  RequestDetail,
  RequestLog,
  RequestPayload,
} from "@/lib/types";
import { useConsole } from "./console-provider";
import { RouteTrail } from "./route-trail";
import { CopyButton, Empty, ErrorBox } from "./shared";

export const requestCharge = (request: RequestLog) =>
  request.billing_mode === "usage_only"
    ? t.noRelayCharge
    : money(request.charge_micros, true);
export const requestCost = (request: RequestLog) =>
  request.cost_status === "untracked" || request.billing_mode === "usage_only"
    ? t.notPriced
    : request.cost_status === "pending"
      ? t.costPending
      : request.cost_status === "recorded"
        ? money(request.cost_micros ?? 0, true)
        : request.cost_micros !== undefined
          ? money(request.cost_micros, true)
          : t.emptyValue;

export function RequestStatus({ request }: { request: RequestLog }) {
  return (
    <Chip
      radius="sm"
      size="sm"
      variant="flat"
      color={
        request.status === "completed" || request.status === "succeeded"
          ? "success"
          : request.error_code
            ? "danger"
            : "default"
      }
    >
      {request.status}
    </Chip>
  );
}

type View = "formatted" | "raw";
// Rendering very large bodies stalls the dialog; the copy button keeps all of it.
const displayLimit = 300_000;
const highlightLimit = 200_000;
const textPreview = 1800;
const roleName = (role: string) => d.roles[role] ?? role;
const timestamp = (value: string) =>
  new Date(value).toLocaleString("en-US", {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });

export function RequestDetailModal({
  request,
  onClose,
}: {
  request: RequestLog;
  onClose: () => void;
}) {
  const { state } = useConsole();
  const isOperator = state.surface === "admin" && state.user.operator;
  const [tab, setTab] = useState("overview");
  const [requestView, setRequestView] = useState<View>("formatted");
  const [responseView, setResponseView] = useState<View>("formatted");
  const [result, setResult] = useState<{ detail?: RequestDetail; error?: string }>();
  const [attempt, setAttempt] = useState(0);
  const [slow, setSlow] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    // Avoid a skeleton flash when the details arrive quickly.
    const timer = setTimeout(() => setSlow(true), 200);
    api<RequestDetail>(
      `/api/requests/${encodeURIComponent(request.id)}`,
      undefined,
      "GET",
      controller.signal,
    )
      .then((detail) => setResult({ detail }))
      .catch((error) => {
        if (!controller.signal.aborted)
          setResult({ error: errorMessage(error) });
      });
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [request.id, attempt]);
  const row = result?.detail?.request ?? request;
  const payload = result?.detail?.payload;
  const reload = () => {
    setResult(undefined);
    setAttempt((n) => n + 1);
  };
  const bodies = (render: (payload: RequestPayload) => ReactNode) => {
    if (!result)
      return slow ? (
        <div role="status" aria-label={d.loading} className="flex flex-col gap-3">
          <Skeleton className="h-6 w-2/3 rounded-[3px]" />
          <Skeleton className="h-32 rounded-[3px]" />
          <Skeleton className="h-32 rounded-[3px]" />
        </div>
      ) : null;
    if (result.error)
      return <ErrorBox message={`${d.loadFailed}: ${result.error}`} retry={reload} />;
    if (!payload && result.detail)
      return <MissingBodies detail={result.detail} onReload={reload} />;
    return payload ? render(payload) : null;
  };
  return (
    // Anchored to the top so switching between tabs of different heights
    // does not move the header.
    <Modal
      isOpen
      onClose={onClose}
      size="3xl"
      placement="top"
      scrollBehavior="inside"
      classNames={{ base: "rp-management-modal rp-request-modal max-h-[min(88dvh,calc(100dvh-5rem))]" }}
    >
      <ModalContent>
        <ModalHeader className="rp-management-modal-header rp-request-header flex-col gap-2">
          <span className="rp-management-modal-eyebrow">
            <Activity size={14} />
            {d.eyebrow}
          </span>
          <h2 className="rp-request-title">{row.model}</h2>
          <div className="rp-request-meta">
            <RequestStatus request={row} />
            <span className="rp-request-id">
              <code>{row.id}</code>
              <CopyButton value={row.id} />
            </span>
            <span>{date(row.created_at)}</span>
          </div>
        </ModalHeader>
        <ModalBody className="rp-management-modal-body rp-request-body">
          <Tabs
            aria-label={d.tabs}
            selectedKey={tab}
            onSelectionChange={(key) => setTab(String(key))}
            variant="underlined"
            color="primary"
            classNames={{ base: "rp-request-tabs", tabList: "rp-request-tab-list", panel: "rp-request-panel" }}
          >
            <Tab key="overview" title={d.overview}>
              <OverviewPanel request={row} payload={payload} isOperator={isOperator} />
            </Tab>
            <Tab key="request" title={d.request}>
              {bodies((p) => (
                <RequestPanel payload={p} view={requestView} onView={setRequestView} />
              ))}
            </Tab>
            <Tab key="response" title={d.response}>
              {bodies((p) => (
                <ResponsePanel payload={p} view={responseView} onView={setResponseView} isOperator={isOperator} />
              ))}
            </Tab>
          </Tabs>
        </ModalBody>
      </ModalContent>
    </Modal>
  );
}

function MissingBodies({
  detail,
  onReload,
}: {
  detail: RequestDetail;
  onReload: () => void;
}) {
  const reload = (label: string) => (
    <Button size="sm" variant="flat" onPress={onReload} startContent={<RefreshCw size={14} />}>
      {label}
    </Button>
  );
  switch (detail.payload_status) {
    case "pending":
      return detail.request.status === "in_progress" ? (
        <Empty title={d.runningTitle} body={d.runningBody} action={reload(t.refresh)} />
      ) : (
        <Empty title={d.savingTitle} body={d.savingBody} action={reload(t.refresh)} />
      );
    case "disabled":
      return <Empty title={d.disabledTitle} body={d.disabledBody} />;
    case "expired": {
      const days =
        detail.retention_days === 1
          ? d.oneDay
          : d.days.replace("{count}", count(detail.retention_days));
      return <Empty title={d.expiredTitle} body={d.expiredBody.replace("{days}", days)} />;
    }
    case "archived":
      return <Empty title={d.archivedTitle} body={d.archivedBody} />;
    case "unavailable":
      return (
        <Empty title={d.unavailableTitle} body={d.unavailableBody} action={reload(t.retry)} />
      );
    default:
      return <Empty title={d.missingTitle} body={d.missingBody} />;
  }
}

function Facts({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="rp-request-facts">
      {rows
        .filter(([, value]) => value !== undefined && value !== "")
        .map(([label, value]) => (
          <div key={label}>
            <dt>{label}</dt>
            <dd>{value}</dd>
          </div>
        ))}
    </dl>
  );
}

function OverviewPanel({
  request,
  payload,
  isOperator,
}: {
  request: RequestLog;
  payload?: RequestPayload;
  isOperator: boolean;
}) {
  const usage = request.usage;
  const tokens = (n: number) => (usage.known ? count(n) : c.unknownUsage);
  const streamed = payload?.response.content_type?.includes("text/event-stream");
  return (
    <>
      <div className="rp-request-summary">
        {[
          [t.inputTokens, tokens(usage.input_tokens)],
          [t.outputTokens, tokens(usage.output_tokens)],
          [t.charge, requestCharge(request)],
          [t.latency, `${count(request.latency_ms)} ms`],
        ].map(([label, value]) => (
          <div key={label}>
            <span>{label}</span>
            <strong>{value}</strong>
          </div>
        ))}
      </div>
      <div className="rp-request-fact-groups">
        <section>
          <h3 className="rp-request-label">{d.requestFacts}</h3>
          <Facts
            rows={[
              [t.requestID, request.id],
              [d.endpoint, payload ? `${payload.request.method} ${payload.request.path}` : undefined],
              [d.stream, payload ? (streamed ? t.yes : t.no) : undefined],
              [t.provider, request.provider],
              [t.status, request.status],
              [t.errorCode, request.error_code],
              [t.key, request.access_key_id],
              ...(isOperator
                ? ([
                    [t.customerID, request.tenant_id],
                    [t.credential, request.credential_id],
                  ] as [string, ReactNode][])
                : []),
              [t.createdAt, timestamp(request.created_at)],
            ]}
          />
        </section>
        <section>
          <h3 className="rp-request-label">{d.usageFacts}</h3>
          <Facts
            rows={[
              [t.cachedTokens, tokens(usage.cached_tokens)],
              [t.cacheWriteTokens, tokens(usage.cache_write_tokens)],
              [t.usageKnown, usage.known ? t.yes : t.no],
              [t.modelBillingMode, request.billing_mode === "usage_only" ? t.modelUsageOnly : t.modelMetered],
              [t.held, money(request.reserved_micros, true)],
              ...(isOperator ? ([[t.cost, requestCost(request)]] as [string, ReactNode][]) : []),
            ]}
          />
          {request.billing_mode === "usage_only" && (
            <p className="rp-request-note">{t.modelUsageOnlyHelp}</p>
          )}
        </section>
      </div>
      <RouteTrail request={request} />
    </>
  );
}

function ViewToggle({ value, onChange }: { value: View; onChange: (view: View) => void }) {
  return (
    <div className="rp-request-toggle" role="group" aria-label={d.bodyView}>
      {(["formatted", "raw"] as const).map((view) => (
        <Button key={view} size="sm" variant="light" aria-pressed={value === view} onPress={() => onChange(view)}>
          {view === "formatted" ? d.formatted : d.raw}
        </Button>
      ))}
    </div>
  );
}

function BodyHead({
  lead,
  tone,
  label,
  meta,
  body,
  view,
  onView,
}: {
  lead: string;
  tone?: "positive" | "danger";
  label: string;
  meta?: string;
  body?: string;
  view?: View;
  onView?: (view: View) => void;
}) {
  return (
    <div className="rp-request-body-head">
      <div className="rp-request-line">
        <p>
          <b data-tone={tone}>{lead}</b>
          <span>{label}</span>
        </p>
        {meta && <span className="rp-request-size">{meta}</span>}
      </div>
      <div className="rp-request-body-tools">
        {view && onView && <ViewToggle value={view} onChange={onView} />}
        {body && <CopyButton value={body} />}
      </div>
    </div>
  );
}

function RecordedNote({ body }: { body: { truncated?: boolean; bytes: number } }) {
  return body.truncated ? (
    <p className="rp-request-note">{d.recordedPart.replace("{total}", formatBytes(body.bytes))}</p>
  ) : null;
}

function RequestPanel({
  payload,
  view,
  onView,
}: {
  payload: RequestPayload;
  view: View;
  onView: (view: View) => void;
}) {
  const sent = payload.request;
  const conversation = useMemo(() => requestConversation(sent.path, sent.body), [sent.path, sent.body]);
  const headers = Object.entries(sent.headers ?? {});
  return (
    <section className="rp-request-section">
      <BodyHead
        lead={sent.method}
        label={sent.path}
        meta={sent.body ? formatBytes(sent.bytes) : undefined}
        body={sent.body || undefined}
        view={sent.body ? view : undefined}
        onView={onView}
      />
      <RecordedNote body={sent} />
      {!sent.body ? (
        <p className="rp-request-note">{d.noRequestBody}</p>
      ) : view === "raw" ? (
        <RawBody body={sent.body} label={d.request} />
      ) : (
        <>
          {headers.length > 0 && (
            <div className="rp-request-group">
              <h3 className="rp-request-label">{d.headers}</h3>
              <KeyValues rows={headers} />
            </div>
          )}
          {conversation ? (
            <ConversationView conversation={conversation} />
          ) : (
            <p className="rp-request-note">{d.notConversation}</p>
          )}
        </>
      )}
    </section>
  );
}

function ConversationView({ conversation }: { conversation: Conversation }) {
  return (
    <>
      {conversation.params.length > 0 && (
        <div className="rp-request-group">
          <h3 className="rp-request-label">{d.parameters}</h3>
          <KeyValues rows={conversation.params} />
        </div>
      )}
      {conversation.tools.length > 0 && (
        <div className="rp-request-group">
          <h3 className="rp-request-label">
            {d.tools} <span>{count(conversation.tools.length)}</span>
          </h3>
          <ul className="rp-request-tools">
            {conversation.tools.map((name, index) => (
              <li key={`${name}-${index}`}>
                <code>{name}</code>
              </li>
            ))}
          </ul>
        </div>
      )}
      <div className="rp-request-group">
        <h3 className="rp-request-label">
          {d.messages} <span>{count(conversation.messageCount)}</span>
        </h3>
        {conversation.turns.length ? (
          <Turns turns={conversation.turns} />
        ) : (
          <p className="rp-request-note">{d.noMessages}</p>
        )}
      </div>
    </>
  );
}

function ResponsePanel({
  payload,
  view,
  onView,
  isOperator,
}: {
  payload: RequestPayload;
  view: View;
  onView: (view: View) => void;
  isOperator: boolean;
}) {
  const got = payload.response;
  const contentType = got.content_type ?? "";
  const output = useMemo(
    () => responseOutput(payload.request.path, contentType, got.body),
    [payload.request.path, contentType, got.body],
  );
  const meta = [
    got.bytes ? formatBytes(got.bytes) : undefined,
    output?.events ? d.events.replace("{count}", count(output.events)) : undefined,
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <>
      <section className="rp-request-section">
        {got.status ? (
          <>
            <BodyHead
              lead={String(got.status)}
              tone={statusTone(got.status)}
              label={contentType}
              meta={meta || undefined}
              body={got.body || undefined}
              view={got.body ? view : undefined}
              onView={onView}
            />
            <RecordedNote body={got} />
            {!got.body ? (
              <p className="rp-request-note">{d.noResponseBody}</p>
            ) : view === "raw" ? (
              <RawBody body={got.body} label={d.response} />
            ) : output ? (
              <OutputView output={output} />
            ) : (
              <p className="rp-request-note">{d.notConversation}</p>
            )}
          </>
        ) : (
          <p className="rp-request-note">{d.noResponse}</p>
        )}
      </section>
      {isOperator && payload.upstream && <UpstreamSection reply={payload.upstream} />}
    </>
  );
}

const statusTone = (status: number) =>
  status >= 200 && status < 300 ? "positive" : status >= 400 ? "danger" : undefined;

function OutputView({ output }: { output: Output }) {
  return (
    <>
      {output.error && (
        <div className="rp-request-error">
          <p className="rp-request-label">{d.returnedError}</p>
          <p>{output.error}</p>
        </div>
      )}
      {output.turns.length > 0 ? (
        <div className="rp-request-group">
          <h3 className="rp-request-label">{d.output}</h3>
          <Turns turns={output.turns} />
        </div>
      ) : (
        !output.error &&
        !output.details.length && (
          <div className="rp-request-group">
            <h3 className="rp-request-label">{d.output}</h3>
            <p className="rp-request-note">{d.noOutput}</p>
          </div>
        )
      )}
      {output.details.length > 0 && (
        <div className="rp-request-group">
          <h3 className="rp-request-label">{d.details}</h3>
          <KeyValues rows={output.details} />
        </div>
      )}
    </>
  );
}

function UpstreamSection({ reply }: { reply: CapturedResponse }) {
  return (
    <section className="rp-request-section rp-request-upstream">
      <h3 className="rp-request-label">{d.upstream}</h3>
      <p className="rp-request-note">{d.upstreamBody}</p>
      <BodyHead
        lead={String(reply.status)}
        tone={statusTone(reply.status)}
        label={reply.content_type ?? ""}
        meta={reply.bytes ? formatBytes(reply.bytes) : undefined}
        body={reply.body || undefined}
      />
      <RecordedNote body={reply} />
      {reply.body ? (
        <RawBody body={reply.body} label={d.upstream} />
      ) : (
        <p className="rp-request-note">{d.noResponseBody}</p>
      )}
    </section>
  );
}

function KeyValues({ rows }: { rows: [string, string][] }) {
  return (
    <dl className="rp-request-kv">
      {rows.map(([key, value], index) => (
        <div key={`${key}-${index}`}>
          <dt>{key}</dt>
          <dd>{value}</dd>
        </div>
      ))}
    </dl>
  );
}

function Turns({ turns }: { turns: Turn[] }) {
  return (
    <ol className="rp-request-turns">
      {turns.map((turn, index) => (
        <li key={index} className="rp-request-turn">
          <p className="rp-request-role">{roleName(turn.role)}</p>
          <div className="rp-request-parts">
            {turn.parts.length ? (
              turn.parts.map((part, i) => <PartView key={i} part={part} role={turn.role} />)
            ) : (
              <p className="rp-request-muted">{d.empty}</p>
            )}
          </div>
        </li>
      ))}
    </ol>
  );
}

function PartView({ part, role }: { part: Part; role: string }) {
  switch (part.kind) {
    case "text":
      return <TextPart text={part.text} tone={part.tone} />;
    case "image":
      return <ImagePart image={part.image} role={role} />;
    case "tool_call":
      return (
        <div className="rp-request-tool">
          <div className="rp-request-tool-head">
            <span>{d.toolCall}</span>
            <code>{part.name}</code>
            {part.id && <code className="rp-request-muted">{part.id}</code>}
          </div>
          <pre>{part.input || "{}"}</pre>
        </div>
      );
    case "tool_result":
      return (
        <div className="rp-request-tool" data-error={part.error || undefined}>
          <div className="rp-request-tool-head">
            <span>{d.toolResult}</span>
            {part.id && <code className="rp-request-muted">{part.id}</code>}
            {part.error && <span className="rp-request-tool-error">{d.toolError}</span>}
          </div>
          <div className="rp-request-tool-body">
            {part.parts.length ? (
              part.parts.map((item, i) => <PartView key={i} part={item} role={role} />)
            ) : (
              <p className="rp-request-muted">{d.empty}</p>
            )}
          </div>
        </div>
      );
    case "block":
      return (
        <div className="rp-request-tool">
          <div className="rp-request-tool-head">
            <code>{part.type}</code>
          </div>
          <pre>{displayValue(part.value)}</pre>
        </div>
      );
  }
}

function TextPart({ text, tone }: { text: string; tone?: "thinking" | "refusal" }) {
  const [open, setOpen] = useState(false);
  const long = text.length > textPreview + 600;
  return (
    <div className="rp-request-text" data-tone={tone}>
      {tone && <p className="rp-request-label">{tone === "thinking" ? d.thinking : d.refusal}</p>}
      {text ? (
        <p className="rp-request-text-body">{long && !open ? `${text.slice(0, textPreview)}…` : text}</p>
      ) : (
        <p className="rp-request-muted">{d.empty}</p>
      )}
      {long && (
        <Button size="sm" variant="light" className="rp-request-more" onPress={() => setOpen(!open)}>
          {open ? d.showLess : d.showAll.replace("{count}", count(text.length))}
        </Button>
      )}
    </div>
  );
}

function ImagePart({ image, role }: { image: ImageSource; role: string }) {
  const [expanded, setExpanded] = useState(false);
  const [size, setSize] = useState("");
  const caption = [image.mediaType, image.bytes ? formatBytes(image.bytes) : undefined, size]
    .filter(Boolean)
    .join(" · ");
  if (!image.src) {
    // Remote images are never fetched from the console.
    const link = image.url && /^https?:\/\//i.test(image.url) ? image.url : undefined;
    return (
      <div className="rp-request-image-missing">
        <ImageOff size={16} strokeWidth={1.6} aria-hidden="true" />
        <div>
          <p>{image.url ? d.remoteImage : d.imageUnavailable}</p>
          {link ? (
            <a href={link} target="_blank" rel="noopener noreferrer">
              {link}
            </a>
          ) : (
            image.url && <code>{image.url.slice(0, 200)}</code>
          )}
          {caption && <span>{caption}</span>}
        </div>
      </div>
    );
  }
  return (
    <figure className="rp-request-image" data-expanded={expanded || undefined}>
      <button
        type="button"
        aria-expanded={expanded}
        aria-label={expanded ? d.collapseImage : d.expandImage}
        onClick={() => setExpanded((value) => !value)}
      >
        {/* eslint-disable-next-line @next/next/no-img-element -- a validated inline data URL from the captured body */}
        <img
          src={image.src}
          alt={d.imageAlt.replace("{role}", roleName(role))}
          onLoad={(event) =>
            setSize(`${event.currentTarget.naturalWidth} × ${event.currentTarget.naturalHeight}`)
          }
        />
      </button>
      {caption && <figcaption>{caption}</figcaption>}
    </figure>
  );
}

const jsonTokens = /("(?:[^"\\\n]|\\.)*")(\s*:)?|\b(?:true|false|null)\b|-?\b\d+(?:\.\d+)?(?:[eE][+-]?\d+)?\b/g;

function Highlighted({ text }: { text: string }) {
  const nodes: ReactNode[] = [];
  let last = 0;
  for (const match of text.matchAll(jsonTokens)) {
    const index = match.index ?? 0;
    if (index > last) nodes.push(text.slice(last, index));
    const [token, quoted, colon] = match;
    const kind = quoted ? (colon ? "key" : "string") : /^[tfn]/.test(token) ? "literal" : "number";
    nodes.push(
      <span key={index} className={`rp-json-${kind}`}>
        {quoted ?? token}
      </span>,
    );
    if (colon) nodes.push(colon);
    last = index + token.length;
  }
  if (last < text.length) nodes.push(text.slice(last));
  return <>{nodes}</>;
}

function RawBody({ body, label }: { body: string; label: string }) {
  const pretty = useMemo(() => displayJSON(body), [body]);
  const text = pretty?.text ?? body;
  const shown = text.length > displayLimit ? text.slice(0, displayLimit) : text;
  return (
    <div className="rp-request-group">
      <pre className="rp-request-raw" tabIndex={0} role="region" aria-label={label}>
        <code>{shown.length <= highlightLimit ? <Highlighted text={shown} /> : shown}</code>
      </pre>
      {pretty?.shortened && <p className="rp-request-note">{d.shortened}</p>}
      {text.length > displayLimit && (
        <p className="rp-request-note">{d.displayLimit.replace("{count}", count(displayLimit))}</p>
      )}
    </div>
  );
}
