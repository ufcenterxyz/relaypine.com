"use client";

import { useEffect, useRef, useState } from "react";
import {
  ArrowRight,
  Check,
  Code2,
  CornerDownRight,
  RotateCcw,
  Route,
} from "lucide-react";
import { useInView, useReducedMotion } from "framer-motion";
import { routingDemo as d } from "@/i18n/routing-demo-en";

type Provider = "openai" | "claude";
type Strategy = "load" | "budget";

// Fixed teaching data. The demo never calls an upstream or reads account data.
const candidates = [
  { id: "01", rpm: 72, budget: 240, proxy: d.proxyA },
  { id: "02", rpm: 24, budget: 90, proxy: d.proxyB },
  { id: "03", rpm: 100, budget: 400, proxy: d.direct },
];

function RoutingScene({
  provider,
  strategy,
  limited,
}: {
  provider: Provider;
  strategy: Strategy;
  limited: boolean;
}) {
  const root = useRef<HTMLDivElement>(null);
  const visible = useInView(root, { once: true, amount: 0.25 });
  const reducedMotion = useReducedMotion();
  const [phase, setPhase] = useState(0);
  const stage = reducedMotion ? 4 : phase;
  const pool = candidates.map((key) => ({
    ...key,
    rpm: limited && key.id === "02" ? 100 : key.rpm,
  }));
  const winner = pool
    .filter((key) => key.rpm < 100 && key.budget > 0)
    .sort((a, b) =>
      strategy === "load" ? a.rpm - b.rpm : b.budget - a.budget,
    )[0];
  const chosen = stage >= 3;
  const complete = stage >= 4;
  const host = provider === "openai" ? "api.openai.com" : "api.anthropic.com";
  const result =
    limited && strategy === "load"
      ? d.limitResult
      : strategy === "budget"
        ? d.budgetResult
        : d.loadResult;

  useEffect(() => {
    if (!visible || reducedMotion) return;
    const timers = [250, 650, 1150, 1700].map((delay, index) =>
      setTimeout(() => setPhase(index + 1), delay),
    );
    return () => timers.forEach(clearTimeout);
  }, [visible, reducedMotion]);

  return (
    <div ref={root} className="rp-route-scene" data-phase={stage}>
      <div className="rp-route-canvas">
        <div className="rp-route-topology" aria-hidden="true">
          <div className="rp-route-inlet">
            <span className="rp-route-micro">{d.request}</span>
            <span className="rp-route-application">
              <Code2 size={17} />
              {d.application}
            </span>
            <code>
              {provider === "openai"
                ? "POST /v1/responses"
                : "POST /v1/messages"}
            </code>
          </div>
          <div className={`rp-route-entry ${stage >= 1 ? "is-active" : ""}`}>
            <span />
            <ArrowRight size={13} />
          </div>
          <div className={`rp-route-gateway ${stage >= 1 ? "is-active" : ""}`}>
            <Route size={25} strokeWidth={1.5} />
            <span>{d.gateway}</span>
          </div>
          <div className="rp-route-checks">
            <span className={stage >= 2 ? "is-checked" : ""}>
              <Check size={12} />
              {d.checkCapacity}
            </span>
            <span className={stage >= 2 ? "is-checked" : ""}>
              <Check size={12} />
              {d.checkBudget}
            </span>
          </div>
        </div>
        <div className="rp-route-branches" aria-hidden="true">
          <svg viewBox="0 0 600 58" preserveAspectRatio="none">
            {pool.map((key, index) => {
              const x = 100 + index * 200;
              const path =
                index === 1
                  ? "M300 0 V58"
                  : `M300 0 V10 Q300 22 ${x < 300 ? 288 : 312} 22 H${x < 300 ? 112 : 488} Q${x} 22 ${x} 34 V58`;
              return (
                <g
                  key={key.id}
                  className={chosen && winner.id === key.id ? "is-chosen" : ""}
                >
                  <path
                    className="rp-route-track"
                    d={path}
                    vectorEffect="non-scaling-stroke"
                  />
                  {chosen && winner.id === key.id && (
                    <path
                      className="rp-route-trace"
                      d={path}
                      pathLength="1"
                      vectorEffect="non-scaling-stroke"
                    />
                  )}
                  <circle cx={x} cy="56" r="3" className="rp-route-terminal" />
                </g>
              );
            })}
          </svg>
        </div>
        <div className="rp-route-key-pool">
          {pool.map((key) => {
            const selected = chosen && key.id === winner.id;
            const full = key.rpm >= 100;
            return (
              <div
                key={key.id}
                className={`rp-route-key ${selected ? "is-selected" : ""} ${full ? "is-limited" : ""}`}
              >
                <div className="rp-route-key-heading">
                  <span>
                    {d.key} {key.id}
                  </span>
                  <span className="rp-route-key-state">
                    {selected ? (
                      <Check size={13} aria-label={d.selected} />
                    ) : (
                      <span />
                    )}
                  </span>
                </div>
                <div className="rp-route-load">
                  <strong>
                    {key.rpm}
                    <small>/100</small>
                  </strong>
                  <span>{d.rpm}</span>
                </div>
                <div className="rp-route-meter" aria-hidden="true">
                  <span style={{ width: `${key.rpm}%` }} />
                </div>
                <div className="rp-route-budget">
                  <span>{d.remaining}</span>
                  <strong>${key.budget}</strong>
                </div>
                <div className="rp-route-key-status">
                  {selected ? d.selected : full ? d.full : d.eligible}
                </div>
              </div>
            );
          })}
        </div>
      </div>
      <aside className="rp-route-decision">
        <p className="rp-route-micro">{d.decision}</p>
        <ol className="rp-route-events">
          {[
            d.receiving,
            d.checking,
            strategy === "load" ? d.selecting : d.selectingBudget,
            d.dispatch,
          ].map((label, index) => (
            <li key={label} className={stage > index ? "is-done" : ""}>
              <span className="rp-route-event-mark">
                {stage > index ? <Check size={11} /> : <span />}
              </span>
              <span>{label}</span>
              <span className="rp-route-event-number">0{index + 1}</span>
            </li>
          ))}
        </ol>
        <div className={`rp-route-result ${complete ? "is-complete" : ""}`}>
          <span className="rp-route-micro">{d.credential}</span>
          <strong>
            {chosen ? `${d.key} ${winner.id}` : "—"}
            <CornerDownRight size={22} />
          </strong>
          <p role="status" aria-live="polite">
            {complete ? result : d.decisionPending}
          </p>
        </div>
        <div className="rp-route-egress">
          <span className="rp-route-micro">{d.egress}</span>
          <span>
            {chosen ? winner.proxy : "—"}
            <ArrowRight size={12} />
          </span>
          <code>{host}</code>
        </div>
      </aside>
    </div>
  );
}

export function RoutingDemo() {
  const [provider, setProvider] = useState<Provider>("openai");
  const [strategy, setStrategy] = useState<Strategy>("load");
  const [limited, setLimited] = useState(false);
  const [run, setRun] = useState(0);
  return (
    <section className="rp-route-demo" aria-label={d.label}>
      <div className="rp-route-heading">
        <div>
          <span className="rp-route-indicator" aria-hidden="true" />
          <h2>{d.title}</h2>
        </div>
        <span className="rp-route-simulation">{d.simulation}</span>
      </div>
      <div className="rp-route-controls">
        <div className="rp-route-control-group">
          <span>{d.provider}</span>
          <div
            className="rp-route-segmented"
            role="group"
            aria-label={d.provider}
          >
            {(["openai", "claude"] as const).map((item) => (
              <button
                type="button"
                key={item}
                aria-pressed={provider === item}
                onClick={() => setProvider(item)}
              >
                {item === "openai" ? d.openai : d.claude}
              </button>
            ))}
          </div>
        </div>
        <div className="rp-route-control-group">
          <span>{d.strategy}</span>
          <div
            className="rp-route-segmented"
            role="group"
            aria-label={d.strategy}
          >
            {(["load", "budget"] as const).map((item) => (
              <button
                type="button"
                key={item}
                aria-pressed={strategy === item}
                onClick={() => setStrategy(item)}
              >
                {item === "load" ? d.load : d.budget}
              </button>
            ))}
          </div>
        </div>
        <button
          type="button"
          className="rp-route-replay"
          onClick={() => setRun((value) => value + 1)}
        >
          <RotateCcw size={13} />
          {d.replay}
        </button>
      </div>
      <RoutingScene
        key={`${provider}-${strategy}-${limited}-${run}`}
        provider={provider}
        strategy={strategy}
        limited={limited}
      />
      <div className="rp-route-demo-actions">
        <label className="rp-route-saturation">
          <input
            type="checkbox"
            checked={limited}
            onChange={(event) => setLimited(event.target.checked)}
          />
          <span className="rp-route-toggle" aria-hidden="true" />
          <span>{d.limit}</span>
        </label>
        <span className="rp-route-limit-help">{d.limitHelp}</span>
      </div>
      <p className="rp-route-demo-note">{d.note}</p>
    </section>
  );
}
