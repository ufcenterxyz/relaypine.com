import type { Model, RequestLog } from "./types";

// GPT Image 2 and 2.5 and their dated snapshots, as the gateway serves them.
const imageModelID = /^(?:gpt-image-2(?:-\d{4}-\d{2}-\d{2})?|gpt-image-2\.5-[a-z]+(?:-\d{4}-\d{2}-\d{2})?)$/;

export function isImageUpstream(provider: string, upstream: string) {
  return provider === "openai" && imageModelID.test(upstream.trim());
}

// A model can serve paid requests. Image models publish no text output price.
export function isPriced(model: Model) {
  if (model.billing_mode === "usage_only") return false;
  if (model.kind === "image")
    return model.input_rate > 0 && (model.image_input_rate ?? 0) > 0 && (model.image_output_rate ?? 0) > 0;
  return model.input_rate > 0 && model.output_rate > 0;
}

// Text and image tokens of a request, without cache reads and writes. An image
// generation tool adds its image model's prompt and image tokens.
export function usageTokens(usage: RequestLog["usage"]) {
  return {
    input: usage.input_tokens + (usage.image_input_tokens ?? 0) + (usage.image_text_tokens ?? 0),
    output: usage.output_tokens + (usage.image_output_tokens ?? 0),
  };
}

export function totalTokens(usage: RequestLog["usage"]) {
  const { input, output } = usageTokens(usage);
  return input + output;
}
