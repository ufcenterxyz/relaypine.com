"use client";

import { useEffect, useRef, useState } from "react";
import { Button, Switch, Tab, Tabs } from "@heroui/react";
import {
  ArrowRight,
  Check,
  Code2,
  KeyRound,
  RotateCcw,
  ShieldCheck,
  TreePine,
} from "lucide-react";
import { useInView, useReducedMotion } from "framer-motion";
import { routingDemo as d } from "@/i18n/routing-demo-en";

type Provider = "openai" | "claude";
type Strategy = "load" | "budget";

// These fixed examples isolate RPM and local budget. Other admission limits
// are assumed available; this is not connected to production telemetry.
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
  const visible = useInView(root, { once: true, amount: 0.35 });
  const reducedMotion = useReducedMotion();
  const [phase, setPhase] = useState(0);
  const pool = candidates.map((key) => ({
    ...key,
    rpm: limited && key.id === "02" ? 100 : key.rpm,
  }));
  const eligible = pool.filter((key) => key.rpm < 100 && key.budget > 0);
  const winner = [...eligible].sort((a, b) =>
    strategy === "load" ? a.rpm - b.rpm : b.budget - a.budget,
  )[0];
  const stage = reducedMotion ? 4 : phase;
  const complete = stage === 4;
  const chosen = stage >= 3;
  const host = provider === "openai" ? "api.openai.com" : "api.anthropic.com";
  const result =
    limited && strategy === "load"
      ? d.limitResult
      : strategy === "budget"
        ? d.budgetResult
        : d.loadResult;
  const status =
    stage < 1
      ? d.receiving
      : stage < 2
        ? d.checking
        : stage < 3
          ? strategy === "load"
            ? d.selecting
            : d.selectingBudget
          : result;

  useEffect(() => {
    if (!visible || reducedMotion) return;
    const timers = [350, 1000, 1700, 2400].map((delay, index) =>
      setTimeout(() => setPhase(index + 1), delay),
    );
    return () => timers.forEach(clearTimeout);
  }, [visible, reducedMotion]);

  return (
    <div ref={root} className="rp-route-scene" data-phase={stage}>
      <div className="rp-route-topology" aria-hidden="true">
        <div className="rp-route-inlet">
          <Code2 size={17} strokeWidth={1.5} />
          <span>
            {d.application}
            <small>{provider === "openai" ? "/responses" : "/messages"}</small>
          </span>
        </div>
        <div className={`rp-route-entry ${stage >= 1 ? "is-active" : ""}`}>
          <span />
        </div>
        <div className={`rp-route-gateway ${stage >= 1 ? "is-active" : ""}`}>
          <TreePine size={28} strokeWidth={1.3} />
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
        <svg viewBox="0 0 600 75" preserveAspectRatio="none">
          {pool.map((key, index) => {
            const x = 100 + 200 * index;
            const path = `M300 0 V12 Q300 30 ${x === 300 ? 300 : x < 300 ? 280 : 320} 30 H${x === 300 ? 300 : x < 300 ? 120 : 480} Q${x} 30 ${x} 50 V75`;
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
                <circle cx={x} cy="73" r="2.5" className="rp-route-terminal" />
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
                <KeyRound size={13} strokeWidth={1.6} />
                <span>
                  {d.key} {key.id}
                </span>
                {selected && (
                  <Check size={13} className="rp-route-selected-check" />
                )}
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
      <div className={`rp-route-outcome ${complete ? "is-complete" : ""}`}>
        <div className="rp-route-outcome-icon">
          <ShieldCheck size={19} strokeWidth={1.5} />
        </div>
        <div className="rp-route-outcome-copy">
          <p role="status" aria-live="polite">
            {status}
          </p>
          <div className="rp-route-egress">
            {chosen ? (
              <>
                <span>{winner.proxy}</span>
                <ArrowRight size={11} />
                <code>{host}</code>
              </>
            ) : (
              <span>{d.reasonFull}</span>
            )}
          </div>
        </div>
      </div>
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
        <h2>{d.title}</h2>
        <span>{d.simulation}</span>
      </div>
      <div className="rp-route-controls">
        <Tabs
          role="group"
          aria-label={d.provider}
          selectedKey={provider}
          onSelectionChange={(key) => setProvider(key as Provider)}
          variant="light"
          size="sm"
          classNames={{
            base: "rp-route-provider-tabs",
            tabList: "gap-0 p-0",
            tab: "h-11 px-4",
            cursor: "bg-background shadow-sm border border-default-200",
            tabContent: "text-xs",
          }}
        >
          <Tab key="openai" title={d.openai} />
          <Tab key="claude" title={d.claude} />
        </Tabs>
        <div className="rp-route-strategy">
          <span>{d.strategy}</span>
          <Tabs
            role="group"
            aria-label={d.strategy}
            selectedKey={strategy}
            onSelectionChange={(key) => setStrategy(key as Strategy)}
            variant="underlined"
            color="primary"
            size="sm"
            classNames={{
              tabList: "gap-3 p-0",
              tab: "h-11 px-0",
              tabContent: "text-[11px]",
              cursor: "w-full",
            }}
          >
            <Tab key="load" title={d.load} />
            <Tab key="budget" title={d.budget} />
          </Tabs>
        </div>
      </div>
      <RoutingScene
        key={`${provider}-${strategy}-${limited}-${run}`}
        provider={provider}
        strategy={strategy}
        limited={limited}
      />
      <div className="rp-route-demo-actions">
        <Switch
          size="sm"
          isSelected={limited}
          onValueChange={setLimited}
          aria-label={d.limit}
          classNames={{
            base: "gap-2 max-w-none py-2 m-0",
            label: "text-[11px]",
            wrapper: "mr-0",
          }}
        >
          {d.limit}
        </Switch>
        <Button
          variant="light"
          size="sm"
          onPress={() => setRun(run + 1)}
          startContent={<RotateCcw size={13} />}
          className="h-11 text-[11px]"
        >
          {d.replay}
        </Button>
      </div>
      <p className="rp-route-demo-note">{d.note}</p>
    </section>
  );
}
