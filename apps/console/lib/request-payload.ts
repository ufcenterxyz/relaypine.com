// Turns captured request and response bodies into a readable conversation for
// Anthropic Messages, OpenAI Chat Completions and OpenAI Responses. Everything
// here is pure and tolerant: an unexpected shape yields null or a generic block,
// and the console falls back to the raw body.

type JSONObject = Record<string, unknown>;

export interface ImageSource {
  /** A validated data: URL for a raster image; safe to render. */
  src?: string;
  mediaType?: string;
  bytes?: number;
  /** A remote reference. It is shown as a link and never loaded automatically. */
  url?: string;
}

export type Part =
  | { kind: "text"; text: string; tone?: "thinking" | "refusal" }
  | { kind: "image"; image: ImageSource }
  | { kind: "tool_call"; name: string; id?: string; input: string }
  | { kind: "tool_result"; id?: string; error?: boolean; parts: Part[] }
  | { kind: "block"; type: string; value: unknown };

export interface Turn {
  role: string;
  parts: Part[];
}

export interface Conversation {
  turns: Turn[];
  /** Entries in the request's message list; system prompts are separate. */
  messageCount: number;
  params: [string, string][];
  tools: string[];
}

export interface Output {
  turns: Turn[];
  details: [string, string][];
  error?: string;
  events?: number;
}

const isObject = (value: unknown): value is JSONObject =>
  typeof value === "object" && value !== null && !Array.isArray(value);
const str = (value: unknown) => (typeof value === "string" ? value : undefined);

export function parseJSON(body: string): unknown {
  try {
    return JSON.parse(body);
  } catch {
    return undefined;
  }
}

export function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

// Only raster formats are rendered. SVG and other types stay as metadata.
const renderableImages = new Set(["image/png", "image/jpeg", "image/gif", "image/webp"]);
const base64Pattern = /^[A-Za-z0-9+/]+={0,2}$/;

function base64Bytes(data: string) {
  const padding = data.endsWith("==") ? 2 : data.endsWith("=") ? 1 : 0;
  return Math.max(0, Math.floor((data.length * 3) / 4) - padding);
}

export function inlineImage(mediaType: unknown, data: unknown): ImageSource {
  const type = str(mediaType)?.toLowerCase();
  if (typeof data !== "string" || !data) return { mediaType: type };
  const bytes = base64Bytes(data);
  if (!type || !renderableImages.has(type) || !base64Pattern.test(data))
    return { mediaType: type, bytes };
  return { src: `data:${type};base64,${data}`, mediaType: type, bytes };
}

export function imageFromURL(url: unknown): ImageSource {
  const value = str(url);
  if (!value) return {};
  const match = /^data:([\w.+-]+\/[\w.+-]+)((?:;[\w.+-]+=[^;,]*)*);base64,/i.exec(value);
  if (match) return inlineImage(match[1], value.slice(match[0].length));
  return { url: value };
}

/** Pretty JSON arguments, or the original text when they are not JSON. */
function prettyArguments(value: unknown) {
  if (typeof value === "string") {
    const parsed = parseJSON(value);
    return parsed === undefined ? value : JSON.stringify(parsed, null, 2);
  }
  return value === undefined ? "" : JSON.stringify(value, null, 2);
}

function partsOf(content: unknown): Part[] {
  if (typeof content === "string") return content ? [{ kind: "text", text: content }] : [];
  if (Array.isArray(content)) return content.flatMap(blockParts);
  if (content === null || content === undefined) return [];
  return [{ kind: "block", type: "content", value: content }];
}

function blockParts(block: unknown): Part[] {
  if (typeof block === "string") return [{ kind: "text", text: block }];
  if (!isObject(block)) return [{ kind: "block", type: typeof block, value: block }];
  const type = str(block.type) ?? "";
  switch (type) {
    case "text":
    case "input_text":
    case "output_text":
    case "summary_text":
      return [{ kind: "text", text: str(block.text) ?? "" }];
    case "refusal":
      return [{ kind: "text", text: str(block.refusal) ?? "", tone: "refusal" }];
    case "thinking":
      return [{ kind: "text", text: str(block.thinking) ?? "", tone: "thinking" }];
    case "image": {
      const source = isObject(block.source) ? block.source : {};
      if (source.type === "base64")
        return [{ kind: "image", image: inlineImage(source.media_type, source.data) }];
      if (source.type === "url") return [{ kind: "image", image: { url: str(source.url) } }];
      break;
    }
    case "image_url":
      return [{ kind: "image", image: imageFromURL(isObject(block.image_url) ? block.image_url.url : block.image_url) }];
    case "input_image":
      if (typeof block.image_url === "string")
        return [{ kind: "image", image: imageFromURL(block.image_url) }];
      break;
    case "tool_use":
      return [{ kind: "tool_call", name: str(block.name) ?? "", id: str(block.id), input: prettyArguments(block.input) }];
    case "tool_result":
      return [{ kind: "tool_result", id: str(block.tool_use_id), error: block.is_error === true, parts: partsOf(block.content) }];
  }
  return [{ kind: "block", type: type || "unknown", value: block }];
}

function toolCall(call: unknown): Part {
  if (!isObject(call)) return { kind: "block", type: "tool_call", value: call };
  const fn = isObject(call.function) ? call.function : isObject(call.custom) ? call.custom : {};
  return { kind: "tool_call", name: str(fn.name) ?? "", id: str(call.id), input: prettyArguments(fn.arguments ?? fn.input) };
}

function chatMessage(message: unknown): Turn {
  if (!isObject(message)) return { role: "unknown", parts: [{ kind: "block", type: "message", value: message }] };
  const role = str(message.role) ?? "assistant";
  const parts: Part[] = [];
  // Compatible providers report reasoning beside the answer.
  for (const key of ["reasoning_content", "reasoning"]) {
    const reasoning = str(message[key]);
    if (reasoning) parts.push({ kind: "text", text: reasoning, tone: "thinking" });
  }
  if (role === "tool")
    parts.push({ kind: "tool_result", id: str(message.tool_call_id), parts: partsOf(message.content) });
  else parts.push(...partsOf(message.content));
  const refusal = str(message.refusal);
  if (refusal) parts.push({ kind: "text", text: refusal, tone: "refusal" });
  if (Array.isArray(message.tool_calls)) parts.push(...message.tool_calls.map(toolCall));
  if (isObject(message.function_call)) parts.push(toolCall({ function: message.function_call }));
  if (Array.isArray(message.images)) parts.push(...message.images.flatMap(blockParts));
  return { role, parts };
}

function responsesItem(item: unknown): Turn {
  if (!isObject(item)) return { role: "user", parts: partsOf(item) };
  const type = str(item.type) ?? "message";
  switch (type) {
    case "message":
      return { role: str(item.role) ?? "user", parts: partsOf(item.content) };
    case "function_call":
    case "custom_tool_call":
      return { role: "assistant", parts: [{ kind: "tool_call", name: str(item.name) ?? "", id: str(item.call_id), input: prettyArguments(item.arguments ?? item.input) }] };
    case "function_call_output":
    case "custom_tool_call_output":
      return { role: "tool", parts: [{ kind: "tool_result", id: str(item.call_id), parts: partsOf(item.output) }] };
    case "reasoning": {
      const summary = Array.isArray(item.summary) ? item.summary.flatMap(blockParts) : [];
      const parts = summary.map((part): Part => (part.kind === "text" ? { ...part, tone: "thinking" } : part));
      return { role: "assistant", parts: parts.length ? parts : [{ kind: "block", type, value: item }] };
    }
    case "image_generation_call":
      if (typeof item.result === "string" && item.result)
        return { role: "assistant", parts: [{ kind: "image", image: inlineImage("image/png", item.result) }] };
  }
  return { role: "assistant", parts: [{ kind: "block", type, value: item }] };
}

// Output items of one reply read as a single assistant turn.
function mergeTurns(turns: Turn[]) {
  return turns.reduce<Turn[]>((merged, turn) => {
    const last = merged[merged.length - 1];
    if (last && last.role === turn.role) last.parts.push(...turn.parts);
    else merged.push({ role: turn.role, parts: [...turn.parts] });
    return merged;
  }, []);
}

function scalar(value: unknown) {
  if (typeof value === "string") return value;
  const text = JSON.stringify(value) ?? String(value);
  return text.length > 160 ? `${text.slice(0, 159)}…` : text;
}

function toolName(tool: unknown) {
  if (!isObject(tool)) return "";
  return str(tool.name) ?? (isObject(tool.function) ? str(tool.function.name) : undefined) ?? str(tool.type) ?? "";
}

export function requestConversation(path: string, body: string): Conversation | null {
  const value = parseJSON(body);
  if (!isObject(value)) return null;
  const turns: Turn[] = [];
  let omit: string[];
  let messageCount = 0;
  if (path.startsWith("/anthropic/")) {
    const system = partsOf(value.system);
    if (system.length) turns.push({ role: "system", parts: system });
    if (Array.isArray(value.messages)) {
      messageCount = value.messages.length;
      for (const message of value.messages)
        turns.push(isObject(message) ? { role: str(message.role) ?? "user", parts: partsOf(message.content) } : { role: "user", parts: partsOf(message) });
    }
    omit = ["system", "messages", "tools"];
  } else if (path.endsWith("/responses")) {
    const instructions = partsOf(value.instructions);
    if (instructions.length) turns.push({ role: "instructions", parts: instructions });
    if (typeof value.input === "string") {
      messageCount = 1;
      turns.push({ role: "user", parts: partsOf(value.input) });
    } else if (Array.isArray(value.input)) {
      messageCount = value.input.length;
      turns.push(...value.input.map(responsesItem));
    }
    omit = ["instructions", "input", "tools"];
  } else {
    if (Array.isArray(value.messages)) {
      messageCount = value.messages.length;
      turns.push(...value.messages.map(chatMessage));
    }
    omit = ["messages", "tools"];
  }
  return {
    turns,
    messageCount,
    params: Object.entries(value)
      .filter(([key]) => !omit.includes(key))
      .map(([key, item]) => [key, scalar(item)]),
    tools: Array.isArray(value.tools) ? value.tools.map(toolName).filter(Boolean) : [],
  };
}

function errorText(value: unknown): string | undefined {
  if (typeof value === "string") return value || undefined;
  if (!isObject(value)) return undefined;
  return str(value.message) ?? str(value.type) ?? str(value.code) ?? JSON.stringify(value);
}

function detailRows(value: JSONObject, keys: string[]): [string, string][] {
  return keys
    .filter((key) => value[key] !== undefined && value[key] !== null && value[key] !== "")
    .map((key) => [key, scalar(value[key])]);
}

function jsonOutput(path: string, value: JSONObject): Output {
  const error = errorText(value.error);
  if (path.startsWith("/anthropic/")) {
    return {
      turns: Array.isArray(value.content) ? mergeTurns([{ role: str(value.role) ?? "assistant", parts: partsOf(value.content) }]) : [],
      details: detailRows(value, ["stop_reason", "stop_sequence", "input_tokens", "id", "model"]),
      error,
    };
  }
  if (Array.isArray(value.choices)) {
    const choices = value.choices.filter(isObject);
    return {
      turns: mergeTurns(choices.map((choice) => chatMessage(choice.message ?? {}))),
      details: [
        ...choices.flatMap((choice) => detailRows(choice, ["finish_reason"])),
        ...detailRows(value, ["id", "model"]),
      ],
      error,
    };
  }
  if (Array.isArray(value.output)) {
    const incomplete = isObject(value.incomplete_details) ? str(value.incomplete_details.reason) : undefined;
    return {
      turns: mergeTurns(value.output.map(responsesItem)),
      details: [
        ...detailRows(value, ["status"]),
        ...(incomplete ? ([["incomplete_details.reason", incomplete]] as [string, string][]) : []),
        ...detailRows(value, ["id", "model"]),
      ],
      error,
    };
  }
  // Token counts, deletions and gateway errors carry only fields.
  return { turns: [], details: error ? [] : detailRows(value, Object.keys(value).filter((key) => key !== "usage")), error };
}

export interface ServerEvent {
  event?: string;
  data: string;
}

export function parseSSE(body: string): ServerEvent[] {
  const events: ServerEvent[] = [];
  for (const frame of body.split(/\r?\n\r?\n/)) {
    let event: string | undefined;
    const data: string[] = [];
    for (const line of frame.split(/\r?\n/)) {
      if (line.startsWith("event:")) event = line.slice(6).trim();
      else if (line.startsWith("data:")) data.push(line.slice(5).replace(/^ /, ""));
    }
    if (data.length) events.push({ event, data: data.join("\n") });
  }
  return events;
}

function anthropicStream(events: JSONObject[]): Output {
  let message: JSONObject = {};
  const blocks: JSONObject[] = [];
  const toolInput: string[] = [];
  let error: string | undefined;
  for (const event of events) {
    const index = typeof event.index === "number" ? event.index : -1;
    switch (event.type) {
      case "message_start":
        if (isObject(event.message)) message = { ...event.message };
        break;
      case "content_block_start":
        if (index >= 0 && isObject(event.content_block)) blocks[index] = { ...event.content_block };
        break;
      case "content_block_delta": {
        const delta = isObject(event.delta) ? event.delta : {};
        if (index < 0) break;
        const block = (blocks[index] ??= { type: delta.type === "thinking_delta" ? "thinking" : delta.type === "input_json_delta" ? "tool_use" : "text" });
        if (delta.type === "text_delta") block.text = (str(block.text) ?? "") + (str(delta.text) ?? "");
        else if (delta.type === "thinking_delta") block.thinking = (str(block.thinking) ?? "") + (str(delta.thinking) ?? "");
        else if (delta.type === "input_json_delta") toolInput[index] = (toolInput[index] ?? "") + (str(delta.partial_json) ?? "");
        break;
      }
      case "message_delta":
        if (isObject(event.delta)) Object.assign(message, event.delta);
        break;
      case "error":
        error = errorText(event.error) ?? "error";
        break;
    }
  }
  toolInput.forEach((input, index) => {
    if (blocks[index] && input) blocks[index].input = parseJSON(input) ?? input;
  });
  const output = jsonOutput("/anthropic/", { ...message, content: blocks.filter(Boolean) });
  return { ...output, error: error ?? output.error };
}

function chatStream(events: JSONObject[]): Output {
  const choices: JSONObject[] = [];
  const calls: JSONObject[][] = [];
  const head: JSONObject = {};
  let error: string | undefined;
  for (const event of events) {
    if (event.error) error = errorText(event.error);
    if (head.id === undefined && event.id !== undefined) Object.assign(head, { id: event.id, model: event.model });
    if (!Array.isArray(event.choices)) continue;
    for (const chunk of event.choices) {
      if (!isObject(chunk)) continue;
      const index = typeof chunk.index === "number" ? chunk.index : 0;
      const choice = (choices[index] ??= { message: { role: "assistant" } });
      const message = choice.message as JSONObject;
      const delta = isObject(chunk.delta) ? chunk.delta : {};
      if (typeof delta.role === "string") message.role = delta.role;
      for (const key of ["content", "refusal", "reasoning_content", "reasoning"]) {
        const piece = str(delta[key]);
        if (piece) message[key] = (str(message[key]) ?? "") + piece;
      }
      if (Array.isArray(delta.tool_calls)) {
        const choiceCalls = (calls[index] ??= []);
        for (const part of delta.tool_calls) {
          if (!isObject(part)) continue;
          const call = (choiceCalls[typeof part.index === "number" ? part.index : choiceCalls.length] ??= { function: {} });
          if (part.id) call.id = part.id;
          const fn = call.function as JSONObject;
          const piece = isObject(part.function) ? part.function : {};
          for (const key of ["name", "arguments"]) {
            const text = str(piece[key]);
            if (text) fn[key] = (str(fn[key]) ?? "") + text;
          }
        }
      }
      if (Array.isArray(delta.images)) message.images = [...((message.images as unknown[]) ?? []), ...delta.images];
      if (chunk.finish_reason) choice.finish_reason = chunk.finish_reason;
    }
  }
  calls.forEach((choiceCalls, index) => {
    if (choices[index]) (choices[index].message as JSONObject).tool_calls = choiceCalls.filter(Boolean);
  });
  const output = jsonOutput("/openai/", { ...head, choices: choices.filter(Boolean) });
  return { ...output, error: error ?? output.error };
}

function responsesStream(events: JSONObject[]): Output {
  let final: JSONObject | undefined;
  const items: JSONObject[] = [];
  const texts = new Map<string, string>();
  let error: string | undefined;
  for (const event of events) {
    const type = str(event.type) ?? "";
    const index = typeof event.output_index === "number" ? event.output_index : -1;
    if (["response.completed", "response.incomplete", "response.failed"].includes(type) && isObject(event.response)) final = event.response;
    else if (type === "response.output_item.done" && index >= 0 && isObject(event.item)) items[index] = event.item;
    else if (type === "response.output_item.added" && index >= 0 && isObject(event.item)) items[index] ??= event.item;
    else if (type === "response.output_text.delta" && typeof event.item_id === "string")
      texts.set(event.item_id, (texts.get(event.item_id) ?? "") + (str(event.delta) ?? ""));
    else if (type === "error") error = str(event.message) ?? errorText(event.error) ?? "error";
  }
  // Without a terminal event, show finished items and any streamed text.
  const response = final ?? {
    output: items.filter(Boolean).map((item) => {
      const text = texts.get(str(item.id) ?? "");
      const empty = !Array.isArray(item.content) || !item.content.length;
      return item.type === "message" && text && empty ? { ...item, content: [{ type: "output_text", text }] } : item;
    }),
  };
  const output = jsonOutput("/openai/responses", response);
  return { ...output, error: error ?? output.error };
}

export function responseOutput(path: string, contentType: string, body: string): Output | null {
  if (contentType.includes("text/event-stream")) {
    const events = parseSSE(body);
    if (!events.length) return null;
    const objects = events.map((event) => parseJSON(event.data)).filter(isObject);
    const output = path.startsWith("/anthropic/")
      ? anthropicStream(objects)
      : path.endsWith("/responses")
        ? responsesStream(objects)
        : chatStream(objects);
    return { ...output, events: events.length };
  }
  const value = parseJSON(body);
  return isObject(value) ? jsonOutput(path, value) : null;
}

// Opaque binary values that make a body unreadable; their size is enough.
const opaqueKeys = new Set(["data", "b64_json", "result", "file_data", "partial_image_b64", "encrypted_content", "signature"]);

function shorten(key: string, value: string) {
  if (value.length <= 256) return undefined;
  const image = /^data:[^,]{1,100};base64,/i.exec(value);
  if (image) {
    const data = value.slice(image[0].length);
    return `${image[0]}${data.slice(0, 24)}… [${formatBytes(base64Bytes(data))}]`;
  }
  if (opaqueKeys.has(key) && base64Pattern.test(value))
    return `${value.slice(0, 24)}… [${formatBytes(base64Bytes(value))} base64]`;
  return undefined;
}

/** Pretty JSON with long base64 values shortened, or null for other text. */
export function displayJSON(body: string): { text: string; shortened: boolean } | null {
  const value = parseJSON(body);
  if (value === undefined) return null;
  let shortened = false;
  const text = JSON.stringify(
    value,
    (key, item) => {
      if (typeof item !== "string") return item;
      const short = shorten(key, item);
      if (short === undefined) return item;
      shortened = true;
      return short;
    },
    2,
  );
  return { text, shortened };
}

/** Pretty JSON for a single value inside the formatted view. */
export function displayValue(value: unknown) {
  return displayJSON(JSON.stringify(value) ?? "null")?.text ?? String(value);
}
