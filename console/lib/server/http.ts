import { buildErrorBody } from "./errors";

/** JSON response with an explicit status. */
export function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json; charset=utf-8" },
  });
}

/** Localized error response using the documented envelope. */
export function errorResponse(
  errorKey: string,
  status: number,
  locale?: string,
  extra?: Record<string, unknown>,
): Response {
  return jsonResponse(buildErrorBody(errorKey, locale, extra), status);
}

/**
 * Read a required query parameter. Returns the value or undefined when absent
 * or empty.
 */
export function requiredQueryParam(
  searchParams: URLSearchParams,
  name: string,
): string | undefined {
  const value = searchParams.get(name);
  return value && value.length > 0 ? value : undefined;
}

/**
 * Resolve the bank scoping value, accepting a legacy agent-style alias as an
 * alternative to the primary bank id parameter.
 */
export function resolveBankScoping(
  searchParams: URLSearchParams,
): { value: string; fromAlias: boolean } | undefined {
  const primary = requiredQueryParam(searchParams, "bank_id");
  if (primary) return { value: primary, fromAlias: false };
  const alias = requiredQueryParam(searchParams, "agent_id");
  if (alias) return { value: alias, fromAlias: true };
  return undefined;
}

export type RouteContext = {
  request: Request;
  params: Record<string, string | string[] | undefined>;
  locale?: string;
};

export function routeParam(
  context: RouteContext,
  name: string,
): string | undefined {
  const value = context.params[name];
  if (Array.isArray(value)) return value[0];
  return value;
}