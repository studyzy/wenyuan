import type { ServerConfig } from "./config";
import { adminServerConfig } from "./config";
import { encodeBankId } from "./paths";
import { jsonResponse } from "./http";
import {
  SESSION_COOKIE,
  TENANT_SESSION_COOKIE,
  createTenantSessionToken,
  isSecureRequest,
  readSessionToken,
  readTenantSessionToken,
  tenantSessionCookieAttributes,
} from "./session";
import { OPERATOR_SUBJECT } from "./subjects";
import { callUpstream, passthrough, type QueryValue, type UpstreamResult } from "./upstream";
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
  query?: Record<string, QueryValue>;
  method?: string;
  body?: BodyInit;
  /**
   * Whether a tenant session cookie may override the data-plane credential.
   * Data-plane routes keep the default (true); admin routes set false so the
   * tenant key is never sent to the management plane.
   */
  tenantScoped?: boolean;
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

function cookieValue(request: Request, name: string): string | undefined {
  const header = request.headers.get("cookie");
  if (!header) return undefined;
  for (const part of header.split(";")) {
    const [key, ...rest] = part.trim().split("=");
    if (key === name) return rest.join("=");
  }
  return undefined;
}

/**
 * Whether the caller is an operator session with no tenant entered.
 *
 * Such a session holds the console but no data credential: every data-plane
 * request is answered 401 by the API, and a browser client that treats 401 as
 * "signed out" bounces the operator back to the login form. The proxy answers
 * 409 instead, so the client shows "select a tenant" rather than re-running the
 * sign-in it just completed.
 */
function operatorWithoutTenant(config: ServerConfig, request: Request): boolean {
  if (!config.accessKey) return false;
  if (cookieValue(request, TENANT_SESSION_COOKIE)) return false;
  const session = readSessionToken(config.accessKey, cookieValue(request, SESSION_COOKIE), Date.now());
  return session?.sub === OPERATOR_SUBJECT;
}

type TenantOverride = {
  config: ServerConfig;
  setCookie?: string;
  error?: Response;
};

/**
 * Swap the static data-plane credential for the tenant key a console session
 * carries. A tenant cookie that cannot be decrypted or has expired fails closed:
 * falling back to the static key would silently route the request to the
 * deployment's default schema.
 */
async function tenantOverride(config: ServerConfig, request: Request): Promise<TenantOverride> {
  const token = cookieValue(request, TENANT_SESSION_COOKIE);
  if (!token) return { config };
  if (!config.accessKey) {
    return { config, error: jsonResponse({ error: "tenant_session_unavailable" }, 503) };
  }
  const session = readTenantSessionToken(config.accessKey, token, Date.now());
  if (!session) {
    return { config, error: jsonResponse({ error: "tenant_session_invalid" }, 401) };
  }
  let dataplaneKey = session.key;
  let setCookie: string | undefined;
  // Renew shortly before the short-lived key expires, so a console left open
  // keeps working without the browser ever seeing the credential.
  if (session.exp * 1000 - Date.now() < 120_000) {
    const renewed = await renewTenantSession(config, session.tenant_id);
    if (renewed) {
      dataplaneKey = renewed;
      const secure = isSecureRequest(request.headers.get("x-forwarded-proto"));
      const next = createTenantSessionToken(config.accessKey, session.tenant_id, renewed, Date.now());
      setCookie =
        TENANT_SESSION_COOKIE + "=" + next + "; " + tenantSessionCookieAttributes(secure);
    }
  }
  return { config: { ...config, dataplaneKey }, setCookie };
}

async function renewTenantSession(config: ServerConfig, tenantId: string): Promise<string | null> {
  if (!config.adminKey) return null;
  const result = await callUpstream(adminServerConfig(config), {
    path: "/admin/v1/tenants/" + encodeURIComponent(tenantId) + "/session",
    method: "POST",
    headers: { "content-type": "application/json" },
    body: "{}",
  });
  if (!result.ok) return null;
  try {
    const body = JSON.parse(new TextDecoder().decode(result.body)) as { plaintext?: unknown };
    return typeof body.plaintext === "string" ? body.plaintext : null;
  } catch {
    return null;
  }
}

/** Direct-fetch passthrough: upstream status and payload are forwarded verbatim. */
export async function proxyUpstream(options: ProxyOptions): Promise<Response> {
  const override =
    options.tenantScoped === false ? { config: options.config } : await tenantOverride(options.config, options.request);
  if (override.error) return override.error;
  const result = await callUpstream(override.config, forwardBody(options));
  const response = passthrough(result);
  if (override.setCookie) response.headers.append("set-cookie", override.setCookie);
  return response;
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
  const override =
    options.tenantScoped === false ? { config: options.config } : await tenantOverride(options.config, options.request);
  if (override.error) return override.error;
  let result: UpstreamResult;
  try {
    result = await callUpstream(override.config, forwardBody(options));
  } catch {
    return upstreamUnavailableBody(options);
  }
  if (
    result.status === 401 &&
    options.tenantScoped !== false &&
    operatorWithoutTenant(options.config, options.request)
  ) {
    const body = localizeError({
      payload: { error: "tenant_required" },
      errorKey: "api.errors.tenant.required",
      locale: options.locale,
    });
    return new Response(JSON.stringify(body), {
      status: 409,
      headers: { "content-type": "application/json; charset=utf-8" },
    });
  }

  const response = serializeResult(result, options);
  if (override.setCookie) response.headers.append("set-cookie", override.setCookie);
  return response;
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

/**
 * Delete that discards the upstream body and answers with a synthesized
 * success payload at 200.
 *
 * Two routes behave this way rather than passing the upstream body through.
 * Failures still follow the normal rule: the upstream status with the
 * documented error envelope, and 502 when the call never reached the upstream.
 */
export async function serializeSynthesizedDelete(options: SerializeOptions): Promise<Response> {
  let result: UpstreamResult;
  try {
    result = await callUpstream(options.config, forwardBody(options));
  } catch {
    return upstreamUnavailableBody(options);
  }
  if (!result.ok) return serializeResult(result, options);
  return new Response(JSON.stringify({ success: true }), {
    status: 200,
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