export const longContextEn = {
  longContextRates: "Long context rates",
  longContextEnabled: "Charge more for long prompts",
  longContextHelp:
    "Use this when the provider charges more for prompts above a length, as OpenAI does above 272K tokens. A longer prompt moves the whole request, output included, to these rates. The prompt counts cached input and cache writes too.",
  longContextAbove: "Prompts above (tokens)",
  longInputRate: "Long context input",
  longOutputRate: "Long context output",
  longCachedRate: "Long context cached input",
  longCacheWriteRate: "Long context cache writes",
  costLongInputRate: "Upstream long context input cost",
  costLongOutputRate: "Upstream long context output cost",
  costLongCachedRate: "Upstream long context cached input cost",
  costLongCacheWriteRate: "Upstream long context cache write cost",
  thresholdRange: "Enter a whole number of tokens from 1 to 100,000,000.",
  positiveRate: "Enter a rate above $0.",
  belowStandard: "Must be at least the standard rate.",
  over: "Over {tokens}",
  promptsOver: "Prompts over {tokens} tokens",
  noLongContext: "None",
  priceTier: "Price",
  longContextApplied: "Long context rates",
} as const;

type Widen<T> = T extends string ? string : { [K in keyof T]: Widen<T[K]> };
export type LongContextCopy = Widen<typeof longContextEn>;
