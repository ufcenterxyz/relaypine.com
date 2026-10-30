"use client";

import { Button, Checkbox, Chip, Link } from "@heroui/react";
import { ArrowUpRight, Clock3, ShieldCheck } from "lucide-react";
import { pricingEn as p } from "@/i18n/pricing-en";
import { imageEn as im } from "@/i18n/image-en";
import { longContextEn as lc } from "@/i18n/long-context-en";
import { isPriced } from "@/lib/images";
import { compactTokens } from "@/lib/long-context";
import { count, date, money } from "@/lib/api";
import { safeReferenceURL, type PricePreview } from "@/lib/pricing";
import type { LongContext } from "@/lib/types";

const longFields = ["input_rate", "output_rate", "cached_rate", "cache_write_rate"] as const;
const longCosts = ["cost_input_rate", "cost_output_rate", "cost_cached_rate", "cost_cache_write_rate"] as const;

// The long context price of a preview item, after and before, under the
// standard grid. It shows only when either side has one.
function LongContextPrices({ after, before, importMode, labels }: {
  after?: LongContext | null;
  before?: LongContext | null;
  importMode: boolean;
  labels: readonly string[];
}) {
  if (!after && !before) return null;
  const above = after?.above_input_tokens ?? before?.above_input_tokens ?? 0;
  return (
    <div className="mt-3">
      <p className="mb-2 text-xs font-medium text-default-600">{lc.promptsOver.replace("{tokens}", compactTokens(above))}</p>
      <div className="rp-management-price-grid">
        {longFields.map((field, index) => (
          <div key={field} className="rp-management-price-cell">
            <span className="rp-management-price-label">{labels[index]}</span>
            <strong className="rp-management-new-price">{after ? money(after[field], true) : lc.noLongContext}</strong>
            {!importMode && <span className="rp-management-old-price">{p.before}: {before ? money(before[field], true) : lc.noLongContext}</span>}
            {!importMode && <span className="rp-management-old-price">{p.currentCost}: {before ? money(before[longCosts[index]], true) : lc.noLongContext}</span>}
          </div>
        ))}
      </div>
    </div>
  );
}

export function ModelPricePreview({ preview, selected, onSelection, disabled = false }: {
  preview: PricePreview;
  selected: string[];
  onSelection?: (ids: string[]) => void;
  disabled?: boolean;
}) {
  // Image models show text input, image input, image output and cached input.
  const textPrices = {
    rates: ["input_rate", "output_rate", "cached_rate", "cache_write_rate"],
    costs: ["cost_input_rate", "cost_output_rate", "cost_cached_rate", "cost_cache_write_rate"],
    labels: [p.input, p.output, p.cached, p.cacheWrite],
  } as const;
  const imagePrices = {
    rates: ["input_rate", "image_input_rate", "image_output_rate", "cached_rate"],
    costs: ["cost_input_rate", "cost_image_input_rate", "cost_image_output_rate", "cost_cached_rate"],
    labels: [im.textInput, im.imageInput, im.imageOutput, p.cached],
  } as const;
  const importMode = preview.kind === "import";
  return (
    <div className="rp-management-price-preview">
      <div className="rp-management-preview-meta">
        <span>{p.perMillion}</span>
        <span><Clock3 size={12} />{p.checked}: {date(preview.checked_at)}</span>
      </div>
      {onSelection && (
        <div className="rp-management-preview-tools">
          <Button variant="light" size="sm" className="min-h-11" isDisabled={disabled}
            onPress={() => onSelection(preview.items.filter((item) => item.status === "matched").map((item) => item.id))}>
            {p.selectAll}
          </Button>
          <strong role="status">{p.selected.replace("{count}", count(selected.length))}</strong>
        </div>
      )}
      <div className="rp-management-price-items" role="group" aria-label={p.prices}>
        {preview.items.map((item) => {
          const available = item.status !== "unmatched";
          const unchanged = !available && isPriced(item.after);
          const prices = item.after.kind === "image" ? imagePrices : textPrices;
          const sourceURL = safeReferenceURL(item.source_url);
          return (
            <section key={item.id} className="rp-management-price-item" data-selected={selected.includes(item.id)}>
              <div className="rp-management-price-item-heading">
                {onSelection && <Checkbox aria-label={item.name || item.id}
                  isSelected={selected.includes(item.id)} isDisabled={disabled || unchanged}
                  classNames={{ base: "m-0 min-h-10 min-w-8 p-1", label: "sr-only" }}
                  onValueChange={(checked) => onSelection(checked ? [...selected, item.id] : selected.filter((id) => id !== item.id))}>{item.name || item.id}</Checkbox>}
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <h3 className="break-all text-sm font-medium">{item.name || item.id}</h3>
                    <Chip radius="sm" size="sm" variant="flat" color={available ? "default" : "warning"} className="rp-management-price-badge">
                      {item.status === "manual" ? p.manual : available ? p.matched : unchanged ? p.unchanged : importMode ? p.disabled : p.disableAction}
                    </Chip>
                  </div>
                  <p className="mt-1 break-all font-mono text-xs text-default-500">{item.id}</p>
                </div>
              </div>
              {available ? (
                <div className="rp-management-price-grid">
                  {prices.rates.map((field, index) => (
                    <div key={field} className="rp-management-price-cell">
                      <span className="rp-management-price-label">{prices.labels[index]}</span>
                      <strong className="rp-management-new-price">{money(item.after[field] ?? 0, true)}</strong>
                      {!importMode && <span className="rp-management-old-price">{p.before}: {item.before.billing_mode === "usage_only" ? p.noPrice : money(item.before[field] ?? 0, true)}</span>}
                      {!importMode && <span className="rp-management-old-price">{p.currentCost}: {item.before.billing_mode === "usage_only" ? p.noPrice : money(item.before[prices.costs[index]] ?? 0, true)}</span>}
                    </div>
                  ))}
                </div>
              ) : <p className="mt-3 text-xs leading-5 text-default-500">{unchanged ? p.unchangedHelp : item.reason || p.noPriceHelp}</p>}
              {available && item.after.kind !== "image" && (
                <LongContextPrices after={item.after.long_context} before={item.before.long_context} importMode={importMode} labels={textPrices.labels} />
              )}
              {available && <div className="rp-management-price-source"><span>{p.nextRates}</span>{sourceURL && <Link href={sourceURL} target="_blank" rel="noopener noreferrer" size="sm" className="text-xs" color="foreground" underline="hover">
                {item.source_name || p.source}<ArrowUpRight size={12} />
              </Link>}</div>}
            </section>
          );
        })}
      </div>
      {preview.sources.some((source) => source.error) && <p className="text-sm leading-6 text-warning-700">{p.sourcesUnavailable}</p>}
      <div className="rp-management-pricing-note"><ShieldCheck size={17} /><div><strong>{p.financialNote}</strong><p>{importMode ? p.importNote : p.reviewNote}</p><p>{p.routeNote}</p></div></div>
    </div>
  );
}
