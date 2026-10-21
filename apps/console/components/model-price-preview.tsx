"use client";

import { Button, Checkbox, Chip, Link } from "@heroui/react";
import { ArrowUpRight } from "lucide-react";
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
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-default-500">
        <span>{p.perMillion}</span>
        <span>{p.checked}: {date(preview.checked_at)}</span>
      </div>
      {onSelection && (
        <div className="flex flex-wrap items-center justify-between gap-2">
          <Button variant="light" size="sm" className="min-h-11" isDisabled={disabled}
            onPress={() => onSelection(preview.items.filter((item) => item.status === "matched").map((item) => item.id))}>
            {p.selectAll}
          </Button>
          <span className="text-xs text-default-500" role="status">{p.selected.replace("{count}", count(selected.length))}</span>
        </div>
      )}
      <div className="space-y-3" aria-label={p.prices}>
        {preview.items.map((item) => {
          const available = item.status !== "unmatched";
          const unchanged = !available && item.after.input_rate > 0 && item.after.output_rate > 0;
          const sourceURL = safeReferenceURL(item.source_url);
          return (
            <section key={item.id} className="rounded-xl border border-default-200 p-4">
              <div className="flex items-start gap-2">
                {onSelection && <Checkbox aria-label={item.name || item.id}
                  isSelected={selected.includes(item.id)} isDisabled={disabled || unchanged}
                  classNames={{ base: "m-0 min-h-11 min-w-11 p-2" }}
                  onValueChange={(checked) => onSelection(checked ? [...selected, item.id] : selected.filter((id) => id !== item.id))} />}
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <h3 className="break-all text-sm font-medium">{item.name || item.id}</h3>
                    <Chip size="sm" variant="flat" color={available ? "default" : "warning"}>
                      {item.status === "manual" ? p.manual : available ? p.matched : unchanged ? p.unchanged : importMode ? p.disabled : p.disableAction}
                    </Chip>
                  </div>
                  <p className="mt-1 break-all font-mono text-xs text-default-500">{item.id}</p>
                </div>
              </div>
              {available ? (
                <div className="mt-4 grid grid-cols-2 gap-x-4 gap-y-3 sm:grid-cols-4">
                  {rateFields.map((field, index) => (
                    <div key={field}>
                      <p className="text-xs text-default-500">{labels[index]}</p>
                      {!importMode && <p className="mt-1 text-xs tabular-nums text-default-500">{p.before}: {item.before.billing_mode === "usage_only" ? p.noPrice : money(item.before[field], true)}</p>}
                      {!importMode && <p className="mt-1 text-xs tabular-nums text-default-500">{p.currentCost}: {item.before.billing_mode === "usage_only" ? p.noPrice : money(item.before[costFields[index]], true)}</p>}
                      <p className="mt-1 text-sm font-medium tabular-nums">{money(item.after[field], true)}</p>
                    </div>
                  ))}
                </div>
              ) : <p className="mt-3 text-xs leading-5 text-default-500">{unchanged ? p.unchangedHelp : item.reason || p.noPriceHelp}</p>}
              {sourceURL && <Link href={sourceURL} target="_blank" rel="noopener noreferrer" size="sm" className="mt-3 min-h-11 text-xs" color="foreground" underline="hover">
                {item.source_name || p.source}<ArrowUpRight size={13} className="ml-1" />
              </Link>}
            </section>
          );
        })}
      </div>
      {preview.sources.some((source) => source.error) && <p className="text-sm leading-6 text-warning-700">{p.sourcesUnavailable}</p>}
      <p className="text-xs leading-5 text-default-500">{p.routeNote}</p>
      <p className="text-xs leading-5 text-default-500">{importMode ? p.importNote : p.reviewNote}</p>
    </div>
  );
}
