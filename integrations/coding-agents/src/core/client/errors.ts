/**
 * The typed failures the client hands its callers, plus the Retry-After parser the backoff uses.
 */


/**
 * Raised when the API rate-limits a request (HTTP 429), carrying how long it asked us to wait.
 *
 * A plain Error would be indistinguishable from a 500 or a network fault, and those want opposite
 * treatment: a rate limit is a "not now, ask again" that the caller can honour, while a server
 * error is not worth hammering. Callers with somewhere safe to wait retry; the rest fail as before.
 */
export class RateLimitedError extends Error {
  readonly code = "rate_limited";
  constructor(readonly retryAfterMs: number) {
    super(`rate limited; retry after ${Math.round(retryAfterMs / 1000)}s`);
    this.name = "RateLimitedError";
  }
}

/** Parse a Retry-After header (delta-seconds or HTTP-date) into ms; 0 when absent or unusable. */
export function retryAfterMs(header: string | null | undefined): number {
  if (!header) return 0;
  const secs = Number(header.trim());
  if (Number.isFinite(secs) && secs >= 0) return secs * 1000;
  const at = Date.parse(header);
  return Number.isNaN(at) ? 0 : Math.max(0, at - Date.now());
}

/** Raised when the target server predates the knowledge-pages API surface. */
export class KnowledgePagesUnavailableError extends Error {
  readonly code = "knowledge_pages_unavailable";
  constructor() {
    super("WenYuan server does not support knowledge pages");
    this.name = "KnowledgePagesUnavailableError";
  }
}

/**
 * A reflect that failed on the SERVER's side of the wire: our deadline expired or the server
 * answered non-2xx. status is undefined for a timeout. Transport errors (connection refused, DNS)
 * are NOT wrapped — the server is unreachable, so no other endpoint would answer either.
 */
export class ReflectError extends Error {
  constructor(
    message: string,
    readonly status: number | undefined,
    readonly timedOut: boolean,
    options?: ErrorOptions
  ) {
    super(message, options);
    this.name = "ReflectError";
  }

  /** A timeout or a 5xx means reflect's synthesis (the slow LLM path) broke, while the cheap
   *  retrieval endpoints may still answer — worth falling back. A 4xx will fail the same way on
   *  every endpoint (auth, missing bank), so it is not. */
  get fallbackEligible(): boolean {
    return this.timedOut || (this.status !== undefined && this.status >= 500);
  }

  /** A 4xx the next attempt would get again (bad key, missing bank, rejected body) — retrying it
   *  only spends another doomed call. 408/429 are "not now", so they stay retryable. */
  get permanent(): boolean {
    const s = this.status;
    return s !== undefined && s >= 400 && s < 500 && s !== 408 && s !== 429;
  }
}

/**
 * Does this 404 mean the server has no knowledge-base API, rather than "that bank does not exist
 * yet"? The two are indistinguishable by status, and conflating them latches the knowledge-page
 * capability off for the whole process on the FIRST session in a new bank — the bank is minted by
 * the first retain, so a session-start page read always precedes it.
 *
 * It matches the ENDPOINT-missing shape (the API answers an unrouted path with exactly
 * {"detail":"Not Found"}), deliberately NOT the bank-missing wording: matching the bank error
 * instead would hang the fix on a message the API is free to rephrase. A reworded bank error
 * simply fails to match — the capability stays unknown and the next call retries, which is the
 * safe direction to be wrong in.
 */
export async function isEndpointMissing(r: Response): Promise<boolean> {
  try {
    // Consumes the body, which is safe: every 404 caller returns without reading it otherwise.
    const j = (await r.json()) as { detail?: unknown };
    return (
      String(j?.detail ?? "")
        .trim()
        .toLowerCase() === "not found"
    );
  } catch {
    // No JSON body at all (a proxy's HTML/plain-text 404 during a backend restart). Only the
    // API's own JSON 404 is a verdict; anything else leaves the capability unknown, so the next
    // call asks again.
    return false;
  }
}
