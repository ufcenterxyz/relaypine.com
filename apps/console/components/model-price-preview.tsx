"use client";

import { Button, Checkbox, Chip, Link } from "@heroui/react";
import { ArrowUpRight, Clock3, ShieldCheck } from "lucide-react";
import { pricingEn as p } from "@/i18n/pricing-en";
import { count, date, money } from "@/lib/api";
import { safeReferenceURL, type PricePreview } from "@/lib/pricing";

export function ModelPricePreview({ preview, selected, onSelection, disabled = false }: {
  preview: PricePreview;
  selected: string[];
  onSelection?: (ids: string[]) => void;
  disabled?: boolean;
}) {
  const rateFields = ["input_rate", "output_rate", "cached_rate", "cache_write_rate"] as const;
  const costFields = ["cost_input_rate", "cost_output_rate", "cost_cached_rate", "cost_cache_write_rate"] as const;
  const labels = [p.input, p.output, p.cached, p.cacheWrite];
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
          const unchanged = !available && item.after.input_rate > 0 && item.after.output_rate > 0;
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
                  {rateFields.map((field, index) => (
                    <div key={field} className="rp-management-price-cell">
                      <span className="rp-management-price-label">{labels[index]}</span>
                      <strong className="rp-management-new-price">{money(item.after[field], true)}</strong>
                      {!importMode && <span className="rp-management-old-price">{p.before}: {item.before.billing_mode === "usage_only" ? p.noPrice : money(item.before[field], true)}</span>}
                      {!importMode && <span className="rp-management-old-price">{p.currentCost}: {item.before.billing_mode === "usage_only" ? p.noPrice : money(item.before[costFields[index]], true)}</span>}
                    </div>
                  ))}
                </div>
              ) : <p className="mt-3 text-xs leading-5 text-default-500">{unchanged ? p.unchangedHelp : item.reason || p.noPriceHelp}</p>}
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
