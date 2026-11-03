/**
 * The option and response shapes the client's public surface is written against.
 */

import type { CurrentPageTrigger } from "../missions";
import type { ObservationScopes } from "./constants";

/** One node of GET /knowledge-base/tree. Only the fields this client reads. */
export interface KnowledgeNode {
  id: string;
  kind: "folder" | "page";
  name: string;
  /** The page's source query — what a re-sync compares against. */
  description?: string;
  /** The page's EFFECTIVE refresh policy, on servers new enough to report it. Absent everywhere
   *  else, which the page seeder reads as "unknown, leave it alone". */
  trigger?: CurrentPageTrigger;
  children?: KnowledgeNode[];
}

export interface ClientOpts {
  apiUrl: string;
  apiToken?: string;
  bank: string;
  /** Repository this bank is about, named in every page query this client writes
   *  (pageScopeRule). Undefined when no single repository is (a shared static bank, a path map,
   *  a bank several repos are renamed onto) — the query then gets the shared-bank clause, which
   *  names no project. Must be a property of the BANK and never of the calling session's cwd. */
  project?: string;
  log?: (msg: string) => void;
  /** Cap on concurrent retain-related requests (drain op polls, deepen pools). Default 10. */
  maxParallelRetains?: number;
  /** Observation scoping for every retain this client sends. Default DEFAULT_OBSERVATION_SCOPES. */
  observationScopes?: ObservationScopes;
  /** Pages one knowledge-page search returns. Default DEFAULT_PAGE_SEARCH_LIMIT. Lives on the
   *  client so every caller — the hook's injection and the MCP search tool — shares one value. */
  pageSearchLimit?: number;
  /** Recall-body overrides, merged key-by-key over DEFAULT_RECALL_OPTIONS. Passed through to
   *  the API as given (types, max_tokens, budget, ...); query is never overridable. */
  recallOptions?: Record<string, unknown>;
  /** Re-read the bearer token from the LIVE config, for hosts that outlive their credential.
   *  apiToken alone is a construction-time snapshot: a long-lived host kept signing with it
   *  forever, so enabling auth or rotating the key mid-session 401'd every call until the host
   *  restarted. Consulted only on a 401, so the happy path never touches the filesystem. */
  tokenProvider?: () => string | undefined;
  /** How long one request may spend waiting out rate limits (HTTP 429) before it throws
   *  RateLimitedError. Default 0: a hook answers to its host's deadline and must fail fast.
   *  Background work with nobody waiting on it sets this, because a 429 it does not wait out is
   *  an item missing from the bank. */
  rateLimitPatienceMs?: number;
}

export interface RetainOpts {
  /** When the content occurred (temporal ranking). */
  timestamp?: string;
  /** Source provenance (returned with recalls). */
  metadata?: Record<string, string>;
  /** "append" concatenates content onto the stored document instead of replacing it — the
   *  whole point of the live write-back cursor. Requires a document_id and a server that
   *  honours operation_id. */
  updateMode?: "append";
  /** Deterministic operation id: re-submitting the same payload returns the original operation
   *  instead of admitting a second one. Ignored by servers older than 0.8.6. */
  operationId?: string;
}
