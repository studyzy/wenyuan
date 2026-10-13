import { expect } from "vitest";

/**
 * Shared harness for route-handler tests.
 *
 * Route handlers are invoked as plain functions with a standard Request and a
 * params object, so tests exercise the served contract without a running
 * framework server.
 */
export function makeRequest(
  url: string,
  init: RequestInit & { cookie?: string; acceptLanguage?: string; forwardedProto?: string } = {},
): Request {
  const { cookie, acceptLanguage, forwardedProto, headers, ...rest } = init;
  const merged = new Headers(headers as HeadersInit | undefined);
  if (cookie) merged.set("cookie", cookie);
  if (acceptLanguage) merged.set("accept-language", acceptLanguage);
  if (forwardedProto) merged.set("x-forwarded-proto", forwardedProto);
  return new Request(url, { ...rest, headers: merged });
}

export async function readJson(response: Response): Promise<Record<string, unknown>> {
  const text = await response.text();
  return text.length > 0 ? (JSON.parse(text) as Record<string, unknown>) : {};
}

export function expectJsonContentType(response: Response): void {
  expect(response.headers.get("content-type")).toContain("application/json");
}

export const ERROR_KEY_FIELD = "__errorKey";

/** Assert the internal error key never appears in a serialized payload. */
export function expectNoErrorKey(payload: Record<string, unknown>): void {
  const serialized = JSON.stringify(payload);
  expect(serialized).not.toContain(ERROR_KEY_FIELD);
}