/**
 * URL construction for bank-scoped routes.
 *
 * A bank id may contain characters that are meaningful in a path, so it is
 * percent-encoded as a path segment. Everything else the view needs travels as a
 * query parameter, which keeps the path shape stable across views and lets the
 * shell read state back without a route table of its own.
 */

/** The route for a bank, optionally with a query suffix such as `?view=data`. */
export function bankRoute(bankId: string, suffix = ""): string {
  return `/banks/${encodeURIComponent(bankId)}${suffix}`;
}

/**
 * Read the active view from a search string. Falls back when the parameter is
 * absent so a bare bank URL still renders something.
 */
export function readViewParam(
  searchParams: URLSearchParams,
  fallback: string,
): string {
  const value = searchParams.get("view");
  return value && value.length > 0 ? value : fallback;
}

/**
 * Build a search string that preserves the parameters a view does not own, so
 * switching one control does not silently drop another.
 */
export function mergeSearchParams(
  current: URLSearchParams,
  updates: Record<string, string | null>,
): string {
  const next = new URLSearchParams(current);
  for (const [key, value] of Object.entries(updates)) {
    if (value === null || value === "") next.delete(key);
    else next.set(key, value);
  }
  const rendered = next.toString();
  return rendered ? `?${rendered}` : "";
}