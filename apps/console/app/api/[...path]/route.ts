import http from "node:http";
import https from "node:https";
import { Readable } from "node:stream";
import type { NextRequest } from "next/server";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

const maxBodyBytes = 1 << 20;
const hopHeaders = new Set([
  "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
  "te", "trailer", "transfer-encoding", "upgrade", "content-length",
]);

function failure(status: number, code: string, message: string) {
  return Response.json({ error: { code, message } }, {
    status, headers: { "Cache-Control": "no-store" },
  });
}

async function proxy(request: NextRequest) {
  const host = request.headers.get("host")?.toLowerCase();
  const configured = [
    process.env.APP_URL || "http://localhost:4810",
    process.env.ADMIN_APP_URL || "http://admin.localhost:4810",
  ];
  let gateway: URL;
  try {
    if (!host || !configured.some((value) => new URL(value).host.toLowerCase() === host))
      return failure(421, "invalid_host", "This host is not configured.");
    gateway = new URL(process.env.GATEWAY_URL || "http://127.0.0.1:4811");
    if (!["http:", "https:"].includes(gateway.protocol) || gateway.username || gateway.password)
      throw new Error("Invalid gateway configuration");
  } catch {
    return failure(503, "proxy_unavailable", "The console connection is unavailable.");
  }
  // The destination comes only from server configuration. Ignore all forwarded host headers.
  gateway.pathname = request.nextUrl.pathname;
  gateway.search = request.nextUrl.search;
  let body: Uint8Array | undefined;
  if (request.body && request.method !== "GET" && request.method !== "HEAD") {
    const reader = request.body.getReader();
    const chunks: Uint8Array[] = [];
    let size = 0;
    try {
      while (true) {
        const item = await reader.read();
        if (item.done) break;
        size += item.value.byteLength;
        if (size > maxBodyBytes) {
          await reader.cancel();
          return failure(413, "body_too_large", "The request is too large.");
        }
        chunks.push(item.value);
      }
      body = new Uint8Array(size);
      let offset = 0;
      for (const chunk of chunks) { body.set(chunk, offset); offset += chunk.byteLength; }
    } catch {
      return failure(400, "invalid_body", "The request could not be read.");
    }
  }
  const headers: Record<string, string> = { host, "accept-encoding": "identity" };
  for (const key of ["accept", "content-type", "cookie", "origin", "user-agent"]) {
    const value = request.headers.get(key);
    if (value !== null) headers[key] = value;
  }
  try {
    // Node's HTTP transport preserves the explicit Host. fetch() rewrites it to
    // the upstream target, which would collapse customer and admin surfaces.
    return await new Promise<Response>((resolve, reject) => {
      const transport = gateway.protocol === "https:" ? https : http;
      const upstream = transport.request(gateway, { method: request.method, headers }, (response) => {
        const outgoing = new Headers();
        const connectionHeaders = new Set((response.headers.connection || "")
          .split(",").map((name) => name.trim().toLowerCase()));
        for (const [key, value] of Object.entries(response.headers)) {
          if (value === undefined || hopHeaders.has(key) || connectionHeaders.has(key)) continue;
          for (const item of Array.isArray(value) ? value : [value]) outgoing.append(key, item);
        }
        outgoing.set("Cache-Control", "no-store");
        response.on("close", () => request.signal.removeEventListener("abort", abort));
        const empty = request.method === "HEAD" || [204, 205, 304].includes(response.statusCode || 200);
        if (empty) response.resume();
        resolve(new Response(empty ? null : Readable.toWeb(response) as ReadableStream<Uint8Array>, {
          status: response.statusCode || 502, headers: outgoing,
        }));
      });
      const abort = () => upstream.destroy(new Error("Request aborted"));
      request.signal.addEventListener("abort", abort, { once: true });
      upstream.setTimeout(30000, () => upstream.destroy(new Error("Gateway timed out")));
      upstream.on("error", (error) => {
        request.signal.removeEventListener("abort", abort);
        reject(error);
      });
      if (request.signal.aborted) abort();
      else upstream.end(body);
    });
  } catch {
    return failure(502, "gateway_unavailable", "The gateway is temporarily unavailable.");
  }
}

export { proxy as GET, proxy as POST, proxy as PUT, proxy as PATCH, proxy as DELETE, proxy as HEAD, proxy as OPTIONS };
