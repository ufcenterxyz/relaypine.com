import type { Model } from "./types";

export interface PricePreviewItem {
  id: string;
  name: string;
  version: number;
  status: "matched" | "unmatched" | "manual";
  reason?: string;
  before: Model;
  after: Model;
  source_url?: string;
  source_name?: string;
  reference_model?: string;
}
export interface PricePreview {
  preview_id: string;
  kind: "update" | "import";
  expires_at: string;
  checked_at: string;
  items: PricePreviewItem[];
  sources: { provider: string; url: string; checked_at: string; error?: string }[];
}
export function validPricePreview(value: unknown): value is PricePreview {
  if (!value || typeof value !== "object") return false;
  const preview = value as PricePreview;
  return typeof preview.preview_id === "string" && !!preview.preview_id &&
    ["update", "import"].includes(preview.kind) &&
    Number.isFinite(Date.parse(preview.expires_at)) && Number.isFinite(Date.parse(preview.checked_at)) &&
    Array.isArray(preview.items) && Array.isArray(preview.sources) &&
    preview.items.every((item) => item && typeof item.id === "string" &&
      ["matched", "unmatched", "manual"].includes(item.status) &&
      item.before && item.after && item.before.id === item.id && item.after.id === item.id &&
      [item.after.input_rate, item.after.output_rate, item.after.cached_rate, item.after.cache_write_rate]
        .every((rate) => Number.isSafeInteger(rate) && rate >= 0)) &&
    new Set(preview.items.map((item) => item.id)).size === preview.items.length;
}
export function safeReferenceURL(value?: string) {
  if (!value) return undefined;
  try {
    const url = new URL(value);
    return url.protocol === "https:" ? url.href : undefined;
  } catch { return undefined; }
}
