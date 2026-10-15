/**
 * URL construction for bank-scoped routes.
 *
 * A bank id may contain characters that are meaningful in a path, so it is
 * percent-encoded as a path segment. Everything else the view needs travels as a
 * query parameter, which keeps the path shape stable across views and lets the
 * shell read state back without a route table of its own.
 */

const enc = (value: string): string => encodeURIComponent(value);

/** The route for a bank, optionally with a query suffix such as `?view=data`. */
export function bankRoute(bankId: string, suffix = ""): string {
  return `/banks/${enc(bankId)}${suffix}`;
}

/** Proxy URL under `/api/banks/...` for a bank-scoped endpoint. */
export function bankApi(bankId: string, suffix = ""): string {
  return `/api/banks/${enc(bankId)}${suffix}`;
}

/** Proxy URL under `/api/stats/...` for bank statistics. */
export function bankStatsApi(bankId: string, suffix = ""): string {
  return `/api/stats/${enc(bankId)}${suffix}`;
}

/** Proxy URL for a memory, scoped to its bank through the query string. */
export function memoryApi(memoryId: string, bankId: string, suffix = ""): string {
  return `/api/memories/${enc(memoryId)}${suffix}${suffix.includes("?") ? "&" : "?"}bank_id=${enc(bankId)}`;
}

/**
 * Proxy URL for a document.
 *
 * A document id is arbitrarily long and routinely contains `/` (S3 keys, file
 * paths), so it travels as a query parameter rather than a path segment: an
 * ingress that decodes `%2F` back to `/` during path normalization would
 * otherwise split the id across segments and miss the route.
 */
export function documentApi(documentId: string, bankId: string, suffix = ""): string {
  return `/api/documents${suffix}?bank_id=${enc(bankId)}&document_id=${enc(documentId)}`;
}

/** Proxy URL for a single chunk. See `documentApi` for why the id is a query parameter. */
export function chunkApi(chunkId: string): string {
  return `/api/chunks?chunk_id=${enc(chunkId)}`;
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