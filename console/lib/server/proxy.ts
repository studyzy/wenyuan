import type { ServerConfig } from "./config";
import { encodeBankId } from "./paths";
import { callUpstream, passthrough, type UpstreamResult } from "./upstream";
import { ERROR_KEY_FIELD, localizeError } from "./errors";
import { negotiateLocale } from "../../i18n/negotiate";

/**
 * Shared upstream proxy helpers for route handlers.
 *
 * Two patterns are supported and must not be conflated:
 *
 * - `serializeUpstream` is the shared SDK serializer. The success status is a
 *   per-route argument (200 by default, 201/202 where the route states one)
 *   rather than a passthrough of the upstream success status. On failure the
 *   response carries the upstream status, or 502 when no upstream status is
 *   present, with the documented error body.
 * - `proxyUpstream` is the direct-fetch passthrough used only by routes that
 *   genuinely forward the upstream payload and status verbatim (binary
 *   streams, verbatim template and probe payloads).
 */
export type ProxyOptions = {
  config: ServerConfig;
  request: Request;
  upstreamPath: string;
  query?: Record<string, string | undefined>;
  method?: string;
  body?: BodyInit;
};

export type SerializeOptions = ProxyOptions & {
  /** Per-route success status. 200 by default; 201 or 202 where the route states one. */
  successStatus?: number;
  /** Operation label reported as the `error` field on the failure path. */
  failureLabel: string;
  /** Localization key attached as `errorKey` only when the label is mapped. */
  errorKey?: string;
  /** Locale used for the localized failure envelope. */
  locale?: string;
};

export function requestLocale(request: Request): string {
  return negotiateLocale({
    cookieHeader: request.headers.get("cookie") ?? undefined,
    acceptLanguage: request.headers.get("accept-language") ?? undefined,
  });
}

export function bankScopedPath(prefix: string, bankId: string, suffix = ""): string {
  return `${prefix}/${encodeBankId(bankId)}${suffix}`;
}

function forwardBody(options: ProxyOptions) {
  return {
    path: options.upstreamPath,
    method: options.method ?? options.request.method,
    query: options.query,
    headers: { "content-type": options.request.headers.get("content-type") ?? "application/json" },
    body: options.body,
  } as const;
}

/** Direct-fetch passthrough: upstream status and payload are forwarded verbatim. */
export async function proxyUpstream(options: ProxyOptions): Promise<Response> {
  const result = await callUpstream(options.config, forwardBody(options));
  return passthrough(result);
}

function decodedUpstreamText(result: UpstreamResult): string | undefined {
  if (result.body.byteLength === 0) return undefined;
  try {
    return new TextDecoder().decode(result.body);
  } catch {
    return undefined;
  }
}

function upstreamErrorField(result: UpstreamResult): string | null {
  const text = decodedUpstreamText(result);
  if (!text) return null;
  try {
    const decoded = JSON.parse(text) as Record<string, unknown>;
    const candidate = decoded.error ?? decoded.message ?? decoded.detail;
    if (typeof candidate === "string") return candidate;
    if (candidate !== undefined) return JSON.stringify(candidate);
  } catch {
    return text;
  }
  return null;
}

/**
 * Shared SDK serializer.
 *
 * Success: the caller-chosen per-route status with the upstream JSON payload.
 * Failure: the upstream status, or 502 when none is present. The body carries
 * `error` (the failure label), an `errorKey` only when one is mapped, a
 * `details` field that is the upstream error only for 4xx and null otherwise,
 * and a nested `upstream` object with `status` and a detail that follows the
 * same 4xx-only rule.
 *
 * A transport failure (the call itself throws) has no upstream status at all,
 * so it reports 502 rather than being classified as an internal error. Callers
 * therefore do not need a catch that guesses a status.
 */
export async function serializeUpstream(options: SerializeOptions): Promise<Response> {
  let result: UpstreamResult;
  try {
    result = await callUpstream(options.config, forwardBody(options));
  } catch {
    return upstreamUnavailableBody(options);
  }
  return serializeResult(result, options);
}

export function serializeResult(result: UpstreamResult, options: SerializeOptions): Response {
  const status = options.successStatus ?? 200;

  if (result.ok) {
    return new Response(result.body, {
      status,
      headers: { "content-type": result.contentType ?? "application/json; charset=utf-8" },
    });
  }

  const upstream: Record<string, unknown> = {
    status: result.status,
    detail: result.status >= 400 && result.status < 500 ? upstreamErrorField(result) : null,
  };
  const body: Record<string, unknown> = {
    error: options.failureLabel,
    details: result.status >= 400 && result.status < 500 ? upstreamErrorField(result) : null,
    upstream,
  };
  if (options.errorKey) body[ERROR_KEY_FIELD] = options.errorKey;

  const localized = localizeError({
    payload: body,
    errorKey: options.errorKey ?? options.failureLabel,
    locale: options.locale,
  });

  return new Response(JSON.stringify(localized), {
    status: result.status,
    headers: { "content-type": "application/json; charset=utf-8" },
  });
}

/**
 * Failure response for the shared serializer when no upstream status is
 * available at all: 502 with the documented body shape.
 */
export function upstreamUnavailableBody(
  options: Pick<SerializeOptions, "failureLabel" | "errorKey" | "locale">,
): Response {
  const body: Record<string, unknown> = {
    error: options.failureLabel,
    details: null,
    upstream: { status: null, detail: null },
  };
  if (options.errorKey) body[ERROR_KEY_FIELD] = options.errorKey;
  const localized = localizeError({
    payload: body,
    errorKey: options.errorKey ?? options.failureLabel,
    locale: options.locale,
  });
  return new Response(JSON.stringify(localized), {
    status: 502,
    headers: { "content-type": "application/json; charset=utf-8" },
  });
}

export function searchParamsObject(request: Request): Record<string, string> {
  const result: Record<string, string> = {};
  new URL(request.url).searchParams.forEach((value, key) => {
    result[key] = value;
  });
  return result;
}