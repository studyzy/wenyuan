/**
 * Wire-level constants and types the client's parts share.
 */

import { version as PKG_VERSION } from "../../../package.json";

/** Sent on every request so the server can tell which integration is calling. */
export const USER_AGENT = `wenyuan-coding-agents/${PKG_VERSION}`;

// The retrieval-shaping vocabulary and the two numbers below are CONFIG's to own (see
// src/core/config/scopes.ts); the client re-exports them so the two cannot drift.
import { DEFAULT_OBSERVATION_SCOPES, DEFAULT_PAGE_SEARCH_LIMIT, type ObservationScopes } from "../config";

export { DEFAULT_OBSERVATION_SCOPES, DEFAULT_PAGE_SEARCH_LIMIT };
export type { ObservationScopes };


/**
 * per_source is resolved per document, client-side, and never reaches the server in that form:
 * it expands to the global scope plus one named for that document's own source: tag.
 *
 * It cannot be expressed as configuration. The server treats an explicit scope list as
 * unconditional — it returns it verbatim without filtering against the memory's own tags — so a
 * configured [[], ["source:git"], ["source:chat"]] writes EVERY document into all three, and the
 * source:git scope fills up with beliefs built from chat transcripts. Only a per-document
 * decision separates "what the commits say" from "what was discussed".
 *
 * per_tag would split on the right axis but also on every other one: it reinstates the per-agent
 * harness: fork that "shared" exists to prevent, and any volatile tag (a session id in
 * retainTags) becomes its own scope — the fragmentation bug itself. Reading only source: keeps
 * this safe. A document may carry more than one source tag, so every distinct one gets a scope;
 * sorting is the only rule that needs no tie-break. The empty scope is always first and always
 * present, so the untagged observations knowledge pages read keep being written exactly as
 * under "shared".
 */
export function resolveRetainScopes(
  tags: string[] | undefined,
  configured: ObservationScopes
): ObservationScopes {
  if (configured !== "per_source") return configured;
  const sources = [...new Set((tags ?? []).filter((t) => t.startsWith("source:")))].sort();
  return [[], ...sources.map((s) => [s])];
}

/** First release whose retain endpoint honours a caller-supplied operation_id. Below it the field
 *  is silently ignored, so an append could be applied twice without anyone knowing — hence: no
 *  idempotency, no append. */
export const MIN_IDEMPOTENT_RETAIN_VERSION = "0.8.6";

/** Default cap on concurrent retain-related requests (drain polls, deepen pools). */
export const DEFAULT_MAX_PARALLEL_RETAINS = 10;


/**
 * The recall body sent when recallOptions overrides nothing — one object rather than a field per
 * parameter, so a new recall parameter needs no plumbing here.
 *
 * Observations are the consolidated layer, so they are the best answer per token; a bank whose
 * consolidation is off never grows any, and such a bank widens this (types: null recalls every
 * type). The budget stays low and entities are excluded because this runs inside a hook window.
 */
export const DEFAULT_RECALL_OPTIONS: Record<string, unknown> = {
  types: ["observation"],
  budget: "low",
  max_tokens: 2000,
  include: { entities: null },
};

/** Hard cap on every ordinary request: a stalled server must degrade to a memoryless turn,
 *  never hang a host that awaits us. */
export const DEFAULT_REQUEST_TIMEOUT_MS = 15_000;

/** How long drain() pauses between poll cycles when the API did not rate-limit (429). */
const POLL_CYCLE_MS = 5000;

/** Consecutive 5xx polls after which drain() gives an operation up as failed. A blip recovers in
 *  one or two cycles; a server answering 5xx this long (about a minute at the 5s cycle) is not
 *  going to report that op's status, and polling it for the rest of the hour only hides it. */
const POLL_MAX_SERVER_ERRORS = 12;

/** Minimum backoff after a 429 that carried no (or a shorter) Retry-After. */
const RETRY_AFTER_FLOOR_MS = 10 * 1000;

/**
 * Ceiling on a single backoff, however long Retry-After asks for.
 *
 * The header is a server's hint, not a budget owed to it: a large value (an incident, a
 * misconfigured limiter, a proxy inventing one) would otherwise park a drain for as long as it
 * says. Capping keeps the signal without handing over the schedule; if the limit still applies,
 * the next poll simply gets another 429 and backs off again.
 */
const RETRY_AFTER_CEILING_MS = 60 * 1000;

/** First wait when a request is rate-limited and Retry-After asks for less (a limiter can send
 *  "0"), doubled per attempt up to the ceiling. Jittered, so a pool of workers that all got the
 *  429 together does not come back together. */
const RATE_LIMIT_BACKOFF_MS = 1000;

const TERMINAL = new Set(["completed", "failed", "cancelled", "error"]);

export {
  POLL_CYCLE_MS,
  POLL_MAX_SERVER_ERRORS,
  RATE_LIMIT_BACKOFF_MS,
  RETRY_AFTER_CEILING_MS,
  RETRY_AFTER_FLOOR_MS,
  TERMINAL,
};
