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
 * Collect the named boolean query flags.
 *
 * A flag is set only when its raw value is exactly the string "true"; every
 * other value leaves the flag unset so the upstream default applies. Unset
 * flags are omitted rather than sent as false.
 */
export function booleanQueryFlags(
  request: Request,
  names: readonly string[],
): Record<string, string> {
  const searchParams = new URL(request.url).searchParams;
  const result: Record<string, string> = {};
  for (const name of names) {
    if (searchParams.get(name) === "true") result[name] = "true";
  }
  return result;
}

/** The only tag-match modes the upstream accepts. */
export const TAGS_MATCH_MODES = ["any", "all", "any_strict", "all_strict", "exact"] as const;

/**
 * Read the repeated `tags` filter and its optional match mode as upstream query
 * parameters.
 *
 * `tags_match` is forwarded only alongside at least one tag, because on its own
 * it would override the upstream default for an otherwise unfiltered read. An
 * unrecognised mode is dropped rather than forwarded, so a typo degrades to a
 * dropped filter instead of an upstream validation error.
 */
export function tagFilterParams(request: Request): Record<string, string | string[]> {
  const searchParams = new URL(request.url).searchParams;
  const tags = searchParams.getAll("tags").filter((tag) => tag.length > 0);
  if (tags.length === 0) return {};
  const result: Record<string, string | string[]> = { tags };
  const mode = searchParams.get("tags_match");
  if (mode && (TAGS_MATCH_MODES as readonly string[]).includes(mode)) {
    result.tags_match = mode;
  }
  return result;
}

/**
 * Read the repeated `tags` filter and its match mode exactly as the caller sent
 * them.
 *
 * This is the unvalidated counterpart of `tagFilterParams`: `tags_match` is
 * forwarded even with no tags present (an `exact` mode on its own selects the
 * upstream's global/untagged scope) and the mode is not checked against the
 * known set. Used by the routes whose upstream accepts the caller's filter
 * verbatim.
 */
export function rawTagFilterParams(request: Request): Record<string, string | string[]> {
  const searchParams = new URL(request.url).searchParams;
  const result: Record<string, string | string[]> = {};
  const tags = searchParams.getAll("tags").filter((tag) => tag.length > 0);
  if (tags.length > 0) result.tags = tags;
  const mode = searchParams.get("tags_match");
  if (mode) result.tags_match = mode;
  return result;
}

/**
 * Forward the named query parameters verbatim, dropping any whose value is
 * empty. Used by the read routes whose upstream accepts the caller's filters
 * unchanged.
 */
export function passthroughQueryParams(
  request: Request,
  names: readonly string[],
): Record<string, string> {
  const searchParams = new URL(request.url).searchParams;
  const result: Record<string, string> = {};
  for (const name of names) {
    const value = searchParams.get(name);
    if (value !== null && value.length > 0) result[name] = value;
  }
  return result;
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