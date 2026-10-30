import type { LongContext } from "./types";

// Short token counts for price labels: 272000 is "272K", 1000000 is "1M".
export function compactTokens(n: number) {
  if (n >= 1_000_000 && n % 1_000_000 === 0) return `${n / 1_000_000}M`;
  if (n >= 1000 && n % 1000 === 0) return `${n / 1000}K`;
  return new Intl.NumberFormat("en-US").format(n);
}

// Long context rates as model form fields, each with the standard rate it may
// not go below and its key in the model's long_context price.
export const longContextRates = [
  { field: "long_input_rate", standard: "input_rate", key: "input_rate" },
  { field: "long_output_rate", standard: "output_rate", key: "output_rate" },
  { field: "long_cached_rate", standard: "cached_rate", key: "cached_rate" },
  { field: "long_cache_write_rate", standard: "cache_write_rate", key: "cache_write_rate" },
  { field: "cost_long_input_rate", standard: "cost_input_rate", key: "cost_input_rate" },
  { field: "cost_long_output_rate", standard: "cost_output_rate", key: "cost_output_rate" },
  { field: "cost_long_cached_rate", standard: "cost_cached_rate", key: "cost_cached_rate" },
  { field: "cost_long_cache_write_rate", standard: "cost_cache_write_rate", key: "cost_cache_write_rate" },
] as const satisfies readonly { field: string; standard: string; key: keyof LongContext }[];

// Form-only keys, which the model API does not accept.
export const longContextFormKeys = ["long_context_enabled", "long_context_above", ...longContextRates.map((rate) => rate.field)];

export const maxLongContextThreshold = 100_000_000;
