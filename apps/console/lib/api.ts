export class ApiError extends Error {
  constructor(
    message: string,
    public status: number,
    public code: string,
  ) {
    super(message);
  }
}
export async function api<T>(
  path: string,
  body?: unknown,
  method = body === undefined ? "GET" : "POST",
): Promise<T> {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 30000);
  try {
    const response = await fetch(path, {
      method,
      credentials: "same-origin",
      headers: body === undefined ? {} : { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: controller.signal,
      cache: "no-store",
    });
    const data = await response.json().catch(() => ({}));
    if (!response.ok)
      throw new ApiError(
        data.error?.message || `Request failed (${response.status})`,
        response.status,
        data.error?.code || "request_failed",
      );
    return data as T;
  } finally {
    clearTimeout(timeout);
  }
}
export function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}
export function money(micros: number, precise = false) {
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: 2,
    maximumFractionDigits: precise ? 6 : 2,
  }).format((micros || 0) / 1e6);
}
export function count(n: number) {
  return new Intl.NumberFormat("en-US").format(n || 0);
}
export function date(value: string) {
  return new Date(value).toLocaleString("en-US", {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}
export const dollars = (value: unknown) => Math.round(Number(value) * 1e6);
