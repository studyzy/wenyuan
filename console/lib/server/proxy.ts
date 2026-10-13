import type { ServerConfig } from "./config";
import { encodeBankId } from "./paths";
import { callUpstream, passthrough } from "./upstream";
import { negotiateLocale } from "../../i18n/negotiate";

/**
 * Shared upstream proxy helper for route handlers.
 *
 * Builds the upstream bank URL with the bank id percent-encoded, forwards the
 * request, and passes the upstream status and payload through on failure while
 * returning the upstream payload on success.
 */
export type ProxyOptions = {
  config: ServerConfig;
  request: Request;
  upstreamPath: string;
  query?: Record<string, string | undefined>;
  method?: string;
  body?: BodyInit;
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

export async function proxyUpstream(options: ProxyOptions): Promise<Response> {
  const result = await callUpstream(options.config, {
    path: options.upstreamPath,
    method: options.method ?? options.request.method,
    query: options.query,
    headers: { "content-type": options.request.headers.get("content-type") ?? "application/json" },
    body: options.body,
  });
  return passthrough(result);
}

export function searchParamsObject(request: Request): Record<string, string> {
  const result: Record<string, string> = {};
  new URL(request.url).searchParams.forEach((value, key) => {
    result[key] = value;
  });
  return result;
}