/**
 * The WenYuan HTTP client for coding-agent integrations (raw fetch, no SDK dependency).
 *
 * Every adapter and the backfill CLI go through this one client: it configures the bank
 * (missions + retain strategies), retains memories, reflects, drains async operations, and
 * creates knowledge pages. Nothing here knows which agent harness is calling.
 *
 * The class owns the state and the one signed-send path; the domain modules under this directory
 * implement each area as functions over it (see transport.ts for the seam).
 */

import {
  DEFAULT_MAX_PARALLEL_RETAINS,
  DEFAULT_OBSERVATION_SCOPES,
  DEFAULT_PAGE_SEARCH_LIMIT,
  DEFAULT_RECALL_OPTIONS,
  DEFAULT_REQUEST_TIMEOUT_MS,
  RATE_LIMIT_BACKOFF_MS,
  RETRY_AFTER_CEILING_MS,
  USER_AGENT,
} from "./constants";
import type { ObservationScopes } from "./constants";
import { retryAfterMs, RateLimitedError } from "./errors";
import {
  activeOperations,
  deleteDocument,
  documentTags,
  drain,
  hasDocuments,
  listDocumentIds,
  retainMemory,
  supportsAppendRetain,
  supportsIdempotentRetain,
} from "./retain";
import { recallObservations, reflect } from "./reflect";
import { sleep } from "../util";
import {
  captureInitiative,
  ensureFolder,
  getPage,
  listPages,
  searchKnowledgePages,
  seedPages,
  tree,
} from "./knowledge";
import { configureBank, type ConfigureBankOpts } from "./bank-config";
import { signalOnlyDispatcher, type ClientInternals } from "./transport";
import type { ClientOpts, KnowledgeNode, RetainOpts } from "./types";

export {
  DEFAULT_MAX_PARALLEL_RETAINS,
  DEFAULT_OBSERVATION_SCOPES,
  DEFAULT_PAGE_SEARCH_LIMIT,
  DEFAULT_RECALL_OPTIONS,
  MIN_IDEMPOTENT_RETAIN_VERSION,
  resolveRetainScopes,
  USER_AGENT,
  type ObservationScopes,
} from "./constants";
export {
  isEndpointMissing,
  KnowledgePagesUnavailableError,
  RateLimitedError,
  ReflectError,
  retryAfterMs,
} from "./errors";
export { shapePage } from "./knowledge";
export { configureBank } from "./bank-config";
export type { ConfigureBankOpts } from "./bank-config";
export type { ClientOpts, KnowledgeNode, RetainOpts } from "./types";
export type { ClientInternals } from "./transport";

/**
 * The client. One instance per resolved bank; every method routes through req() (or, for the
 * long-running calls, fetchWithAuth directly) so 401 recovery and rate-limit patience behave
 * identically everywhere.
 */
export class MemoryClient implements ClientInternals {
  readonly apiUrl: string;
  /** The credential the NEXT request will sign with — NOT the one the config file holds. */
  private token: string | undefined;
  private readonly tokenProvider: (() => string | undefined) | undefined;
  readonly bank: string;
  readonly project: string | undefined;
  readonly opIds: string[] = []; // async operation ids collected by retain(), for drain()
  /** Tri-state capability probe: unknown until the first page request, then cached. */
  knowledgePagesSupported: boolean | undefined;
  /** Tri-state capability probe: unknown until the first append-mode retain, then cached. */
  idempotentRetain: boolean | undefined;
  readonly log: (msg: string) => void;
  readonly maxParallelRetains: number;
  readonly observationScopes: ObservationScopes;
  readonly pageSearchLimit: number;
  readonly recallOptions: Record<string, unknown>;
  private readonly rateLimitPatienceMs: number;

  constructor(o: ClientOpts) {
    this.apiUrl = o.apiUrl.replace(/\/$/, "");
    this.token = o.apiToken;
    this.tokenProvider = o.tokenProvider;
    this.bank = o.bank;
    this.project = o.project;
    this.log = o.log ?? (() => {});
    this.maxParallelRetains = o.maxParallelRetains || DEFAULT_MAX_PARALLEL_RETAINS;
    this.observationScopes = o.observationScopes ?? DEFAULT_OBSERVATION_SCOPES;
    this.pageSearchLimit = o.pageSearchLimit || DEFAULT_PAGE_SEARCH_LIMIT;
    // Merged once here, not per call, and copied rather than aliased: the default is a
    // module-level object, and handing every client the same reference makes one caller's
    // mutation everyone's.
    this.recallOptions = { ...DEFAULT_RECALL_OPTIONS, ...o.recallOptions };
    this.rateLimitPatienceMs = o.rateLimitPatienceMs ?? 0;
  }

  /** The credential in use, for diagnostics. Never log or report the VALUE — booleans only. */
  get apiToken(): string | undefined {
    return this.token;
  }

  private headers(): Record<string, string> {
    // identity: the server gzips larger bodies, and some hosts hand back the compressed bytes
    // undecoded, so .json() dies on the gzip magic. Asking for identity means the server never
    // compresses, whatever fetch the host provides.
    const h: Record<string, string> = {
      "Content-Type": "application/json",
      "Accept-Encoding": "identity",
      "User-Agent": USER_AGENT,
    };
    if (this.token) h["Authorization"] = `Bearer ${this.token}`;
    return h;
  }

  /** Re-read the credential from the live config. Returns whether it actually CHANGED — a retry
   *  is only worth sending if it did, so a genuinely wrong key still surfaces as one 401 rather
   *  than doubling every failing request. */
  private refreshToken(): boolean {
    if (!this.tokenProvider) return false;
    let next: string | undefined;
    try {
      next = this.tokenProvider();
    } catch {
      return false; // a half-written config file must never drop the last credential that worked
    }
    if (next === this.token) return false;
    this.token = next;
    return true;
  }

  /**
   * The ONE place a request is signed. Every fetch goes through it — the generic req, the drain
   * poll and reflect — because a 401 recovery wired into only one of them leaves the others
   * failing forever.
   *
   * On a 401 the credential is re-resolved and the request replayed ONCE (its body is already a
   * string, so replay is exact). A 401 means the server did nothing, so replaying is side-effect
   * free even for retain. The retry shares the caller's signal, deliberately: one deadline still
   * bounds the whole call.
   */
  async fetchWithAuth(url: string, init: RequestInit): Promise<Response> {
    // Only a request with its own deadline drops undici's timeouts; the drain poll has none.
    const dispatcher = init.signal ? signalOnlyDispatcher() : undefined;
    const send = () =>
      fetch(url, {
        ...init,
        headers: this.headers(),
        ...(dispatcher ? { dispatcher } : {}),
      } as RequestInit);
    const r = await send();
    if (r.status !== 401 || !this.refreshToken()) return r;
    return send();
  }

  /** A 401 with no Authorization header is a different failure from a rejected key, and the
   *  server answers identically for both — only the client knows which it sent. */
  authHint(status: number): string {
    if (status !== 401) return "";
    return this.token
      ? " (the configured apiToken was rejected — check ~/.wenyuan/config.json)"
      : " (no apiToken is configured, so no Authorization header was sent)";
  }

  bankUrl(suffix = ""): string {
    return `${this.apiUrl}/v1/default/banks/${encodeURIComponent(this.bank)}${suffix}`;
  }

  /** tolerate adds statuses that are returned to the caller instead of thrown (404 always is). */
  async req(
    method: string,
    url: string,
    body?: unknown,
    tolerate: number[] = [],
    timeoutMs: number = DEFAULT_REQUEST_TIMEOUT_MS
  ): Promise<Response> {
    const payload = body ? JSON.stringify(body) : undefined;
    let r: Response;
    for (let attempt = 0, waited = 0; ; attempt++) {
      r = await this.fetchWithAuth(url, {
        method,
        body: payload,
        signal: AbortSignal.timeout(timeoutMs),
      });
      if (r.status !== 429 || tolerate.includes(429)) break;
      const asked = retryAfterMs(r.headers.get("retry-after"));
      const backoff = Math.min(RETRY_AFTER_CEILING_MS, RATE_LIMIT_BACKOFF_MS * 2 ** attempt);
      const wait =
        Math.min(RETRY_AFTER_CEILING_MS, Math.max(asked, backoff)) * (1 + Math.random() / 2);
      if (waited + wait > this.rateLimitPatienceMs) throw new RateLimitedError(asked);
      await sleep(wait);
      waited += wait;
    }
    if (!r.ok && r.status !== 404 && !tolerate.includes(r.status))
      throw new Error(
        `${method} ${url} -> ${r.status} ${await r.text()}${this.authHint(r.status)}`
      );
    return r;
  }

  // ── retain ────────────────────────────────────────────────────────────────────

  retain(
    content: string,
    context: string,
    documentId: string,
    tags: string[],
    strategy: string,
    opts: RetainOpts = {}
  ): Promise<void> {
    return retainMemory(this, content, context, documentId, tags, strategy, opts);
  }

  supportsIdempotentRetain(): Promise<boolean> {
    return supportsIdempotentRetain(this);
  }

  supportsAppendRetain(): Promise<boolean> {
    return supportsAppendRetain(this);
  }

  listDocumentIds(tag: string, tagsMatch: "all" | "all_strict" = "all"): Promise<Set<string>> {
    return listDocumentIds(this, tag, tagsMatch);
  }

  documentTags(documentId: string): Promise<string[] | undefined> {
    return documentTags(this, documentId);
  }

  hasDocuments(): Promise<boolean> {
    return hasDocuments(this);
  }

  deleteDocument(documentId: string): Promise<void> {
    return deleteDocument(this, documentId);
  }

  activeOperations(): Promise<number> {
    return activeOperations(this);
  }

  drain(ids: string[], label: string, maxMs?: number): Promise<void> {
    return drain(this, ids, label, maxMs);
  }

  // ── reflect / recall ──────────────────────────────────────────────────────────

  reflect(query: string, opts: { budget?: string; timeoutMs: number }): Promise<string> {
    return reflect(this, query, opts);
  }

  recallObservations(query: string, opts: { timeoutMs: number }): Promise<string[]> {
    return recallObservations(this, query, opts);
  }

  // ── knowledge pages ───────────────────────────────────────────────────────────

  tree(): Promise<KnowledgeNode[]> {
    return tree(this);
  }

  listPages(): Promise<unknown> {
    return listPages(this);
  }

  getPage(pageId: string): Promise<unknown> {
    return getPage(this, pageId);
  }

  searchKnowledgePages(
    query: string,
    opts: { limit?: number; timeoutMs?: number } = {}
  ): Promise<{ id: string; name: string; source_query?: string; snippet: string; score: number }[]> {
    return searchKnowledgePages(this, query, opts);
  }

  seedPages(
    pageTrigger?: Parameters<typeof seedPages>[1],
    pagesConfig?: Parameters<typeof seedPages>[2],
    customPages?: Parameters<typeof seedPages>[3]
  ): Promise<void> {
    return seedPages(this, pageTrigger, pagesConfig, customPages);
  }

  captureInitiative(args: Parameters<typeof captureInitiative>[1]): Promise<{ page_id: string }> {
    return captureInitiative(this, args);
  }

  ensureFolder(name: string): Promise<string | undefined> {
    return ensureFolder(this, name);
  }

  // ── bank configuration ────────────────────────────────────────────────────────

  configureBank(opts: ConfigureBankOpts = {}): Promise<void> {
    return configureBank(this, opts);
  }
}
