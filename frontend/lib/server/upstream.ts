import type { ServerConfig } from "./config";

/**
 * Server-side upstream client.
 *
 * Requests are issued only from the server. When an upstream key is configured
 * it is attached as an authorization header; the key never reaches the browser.
 * The upstream status and payload are passed through on failure.
 */
export type UpstreamResult = {
  status: number;
  contentType: string | null;
  contentDisposition: string | null;
  body: ArrayBuffer;
  ok: boolean;
};

/**
 * A query value may be a single string or a list, because some upstream
 * parameters are repeated rather than overwritten (for example the tag filter).
 */
export type QueryValue = string | readonly string[] | undefined;

export type UpstreamRequest = {
  path: string;
  method?: string;
  query?: Record<string, QueryValue>;
  headers?: Record<string, string>;
  body?: BodyInit;
  signal?: AbortSignal;
  timeoutMs?: number;
};

/**
 * Default upstream deadline. Bounds every proxy request so a stalled upstream
 * cannot pin a server handler open indefinitely.
 */
export const DEFAULT_UPSTREAM_TIMEOUT_MS = 15_000;

export function buildUpstreamUrl(
  config: Pick<ServerConfig, "dataplaneUrl">,
  request: UpstreamRequest,
): string {
  const url = new URL(`${config.dataplaneUrl}${request.path}`);
  if (request.query) {
    for (const [key, value] of Object.entries(request.query)) {
      if (value === undefined) continue;
      if (typeof value === "string") {
        if (value !== "") url.searchParams.set(key, value);
        continue;
      }
      for (const item of value) {
        if (item !== "") url.searchParams.append(key, item);
      }
    }
  }
  return url.toString();
}

export function upstreamHeaders(
  config: Pick<ServerConfig, "dataplaneKey">,
  extra?: Record<string, string>,
): Record<string, string> {
  const headers: Record<string, string> = { ...extra };
  if (config.dataplaneKey) {
    headers["authorization"] = `Bearer ${config.dataplaneKey}`;
    headers["x-api-key"] = config.dataplaneKey;
  }
  return headers;
}

/** Perform an upstream request and normalize the response for passthrough. */
export async function callUpstream(
  config: ServerConfig,
  request: UpstreamRequest,
): Promise<UpstreamResult> {
  const url = buildUpstreamUrl(config, request);
  const timeoutMs = request.timeoutMs ?? DEFAULT_UPSTREAM_TIMEOUT_MS;
  const controller = request.signal ? undefined : new AbortController();
  const timer =
    controller && timeoutMs > 0 ? setTimeout(() => controller.abort(), timeoutMs) : undefined;
  try {
    const response = await fetch(url, {
      method: request.method ?? "GET",
      headers: upstreamHeaders(config, request.headers),
      body: request.body,
      signal: request.signal ?? controller?.signal,
      redirect: "manual",
    });
    const body = await response.arrayBuffer();
    return {
      status: response.status,
      contentType: response.headers.get("content-type"),
      // Forwarded so a proxied file download keeps the upstream filename hint.
      contentDisposition: response.headers.get("content-disposition"),
      body,
      ok: response.ok,
    };
  } finally {
    if (timer) clearTimeout(timer);
  }
}

/** A Response passthrough that carries the upstream status and payload. */
export function passthrough(result: UpstreamResult): Response {
  const headers = new Headers();
  if (result.contentType) headers.set("content-type", result.contentType);
  if (result.contentDisposition) headers.set("content-disposition", result.contentDisposition);
  return new Response(result.body, { status: result.status, headers });
}