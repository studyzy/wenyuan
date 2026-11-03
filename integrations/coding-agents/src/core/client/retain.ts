/**
 * Retain and operation-drain: the write path and the wait-for-it that follows it.
 */

import { pool, semverGte, sleep } from "../util";
import { retryAfterMs } from "./errors";
import {
  MIN_IDEMPOTENT_RETAIN_VERSION,
  POLL_CYCLE_MS,
  POLL_MAX_SERVER_ERRORS,
  RETRY_AFTER_CEILING_MS,
  RETRY_AFTER_FLOOR_MS,
  TERMINAL,
  resolveRetainScopes,
} from "./constants";
import type { RetainOpts } from "./types";
import type { ClientInternals } from "./transport";

/**
 * Retain one memory. ALWAYS async: enqueue extraction server-side and collect its operation id
 * for drain(). Nothing here can afford to block a coding agent's hook on extraction.
 */
export async function retainMemory(
  c: ClientInternals,
  content: string,
  context: string,
  documentId: string,
  tags: string[],
  strategy: string,
  opts: RetainOpts = {}
): Promise<void> {
  const item: Record<string, unknown> = {
    content,
    context,
    document_id: documentId,
    tags,
    strategy,
    // Sent on EVERY retain, including the server default combined, so the scoping a bank's
    // observations were built under is a property of the write rather than of whichever server
    // version happened to process it. Servers older than 0.4.15 ignore the field.
    observation_scopes: resolveRetainScopes(tags, c.observationScopes),
  };
  if (opts.timestamp) item.timestamp = opts.timestamp;
  if (opts.metadata) item.metadata = opts.metadata;
  if (opts.updateMode) item.update_mode = opts.updateMode;
  const body: Record<string, unknown> = { items: [item], async: true };
  if (opts.operationId) body.operation_id = opts.operationId;
  const r = await c.req("POST", c.bankUrl("/memories"), body);
  try {
    const j = (await r.json()) as { operation_id?: string };
    if (j.operation_id) c.opIds.push(j.operation_id);
  } catch {
    /* ignore */
  }
}

/**
 * Whether this server honours operation_id on an async retain, and can therefore be appended to
 * safely. Probed once per client via the version endpoint; anything unreachable, unparseable or
 * older answers "no", which costs efficiency, never correctness.
 */
export async function supportsIdempotentRetain(c: ClientInternals): Promise<boolean> {
  if (c.idempotentRetain === undefined) {
    c.idempotentRetain = await probeIdempotentRetain(c);
    c.log(`server retain idempotency: ${c.idempotentRetain ? "supported" : "unavailable"}`);
  }
  return c.idempotentRetain;
}

/** /v1/version first: an ingress that routes only /v1 to the API hands a bare /version to the
 *  control plane, whose HTML 404 or login redirect read as "no", so append silently never turned
 *  on. Servers that predate the alias still answer /version, so that stays as the fallback. */
async function probeIdempotentRetain(c: ClientInternals): Promise<boolean> {
  for (const path of ["/v1/version", "/version"]) {
    try {
      const r = await c.req("GET", `${c.apiUrl}${path}`);
      if (!r.ok) continue;
      const j = (await r.json()) as { api_version?: string };
      return semverGte(j.api_version, MIN_IDEMPOTENT_RETAIN_VERSION);
    } catch {
      /* unreachable or not our API's JSON — try the next path */
    }
  }
  return false;
}

/**
 * Whether a session write-back may use update_mode="append": the server must dedupe by
 * operation_id AND the bank must keep document text, or the server rejects the append in the
 * background. Only an explicit store_document_text: false answers "no" — an unreachable or
 * unparseable config assumes the default (stored), so a flaky probe never downgrades appends.
 */
export async function supportsAppendRetain(c: ClientInternals): Promise<boolean> {
  if (!(await supportsIdempotentRetain(c))) return false;
  try {
    const r = await c.req("GET", c.bankUrl("/config"));
    if (!r.ok) return true;
    const j = (await r.json()) as { config?: { store_document_text?: boolean } };
    return j.config?.store_document_text !== false;
  } catch {
    return true;
  }
}

/**
 * Every document_id currently in the bank under a strategy tag (e.g. source:git), paginated into
 * a Set. Powers the incremental git-sync's "what is already ingested" check — commits are stored
 * with document_id git:<sha>, so the result diffs a ref's commits against memory.
 */
export async function listDocumentIds(
  c: ClientInternals,
  tag: string,
  tagsMatch: "all" | "all_strict" = "all"
): Promise<Set<string>> {
  const ids = new Set<string>();
  const limit = 500;
  for (let offset = 0; ; offset += limit) {
    const q = `?tags=${encodeURIComponent(tag)}&tags_match=${tagsMatch}&limit=${limit}&offset=${offset}`;
    const r = await c.req("GET", c.bankUrl(`/documents${q}`));
    let items: { id?: string }[] = [];
    let total = 0;
    try {
      const j = (await r.json()) as { items?: { id?: string }[]; total?: number };
      items = j.items || [];
      total = j.total ?? 0;
    } catch {
      break;
    }
    for (const it of items) if (it.id) ids.add(it.id);
    if (items.length < limit || ids.size >= total) break; // last page reached
  }
  return ids;
}

/**
 * The tags on one document, or undefined when the bank holds none with that id. Read from the
 * document LISTING narrowed by q (a substring match on the id) rather than the single-document
 * GET, which also sends the document's full text: the caller, the git-log freshness check on
 * session start, would read a ~100k-character document for one tag.
 */
export async function documentTags(c: ClientInternals, documentId: string): Promise<string[] | undefined> {
  const limit = 100;
  for (let offset = 0; ; offset += limit) {
    const q = `?q=${encodeURIComponent(documentId)}&limit=${limit}&offset=${offset}`;
    const r = await c.req("GET", c.bankUrl(`/documents${q}`));
    const j = (await r.json()) as { items?: { id?: string; tags?: string[] }[]; total?: number };
    const items = j.items ?? [];
    const hit = items.find((it) => it.id === documentId);
    if (hit) return hit.tags ?? [];
    if (items.length < limit || offset + limit >= (j.total ?? 0)) return undefined;
  }
}

/** Whether the bank holds any document at all — a one-row listing. A bank that does not exist
 *  yet (404) holds none; any other failure throws, so a caller can tell "empty" from "unknown". */
export async function hasDocuments(c: ClientInternals): Promise<boolean> {
  const r = await c.req("GET", c.bankUrl("/documents?limit=1"));
  if (r.status === 404) return false;
  const j = (await r.json()) as { items?: unknown[]; total?: number };
  return (j.total ?? j.items?.length ?? 0) > 0;
}

/** Explicitly delete one document (and its cascaded memory units/links). Background sync must
 *  never use this as a cleanup primitive: a document id alone does not prove ownership. */
export async function deleteDocument(c: ClientInternals, documentId: string): Promise<void> {
  await c.req("DELETE", c.bankUrl(`/documents/${encodeURIComponent(documentId)}`));
}

/** Count of operations still ACTIVE on this bank — the list includes terminal ops, so filter by
 *  status. Powers the sync-status "extractions drained" check. */
export async function activeOperations(c: ClientInternals): Promise<number> {
  const r = await c.req("GET", c.bankUrl("/operations"));
  try {
    const j = (await r.json()) as {
      operations?: { status?: string }[];
      items?: { status?: string }[];
    };
    const ops = j.operations ?? j.items ?? [];
    return ops.filter((o) => !TERMINAL.has((o?.status || "").toLowerCase())).length;
  } catch {
    return 0;
  }
}

/**
 * Poll each enqueued operation by id until terminal. The listing only shows active ops, so a
 * per-id GET is the reliable read.
 *
 * Concurrency is capped at maxParallelRetains (the API rate-limits bursts, not single requests).
 * A 429 leaves the op pending and raises the whole next cycle to its Retry-After (10s floor)
 * instead of hammering again 5s later. Any other non-OK is terminal: a 4xx will answer the same
 * next cycle, and a 5xx gets a bounded run of POLL_MAX_SERVER_ERRORS before the op is failed —
 * polling it for the rest of the hour only hides the failure.
 */
export async function drain(
  c: ClientInternals,
  ids: string[],
  label: string,
  maxMs = 60 * 60 * 1000
): Promise<void> {
  if (!ids.length) return;
  c.log(`[wait] draining ${ids.length} ${label} operations ...`);
  const start = Date.now();
  const pending = new Set(ids);
  const serverErrors = new Map<string, number>();
  let failed = 0;
  const giveUp = (id: string, status: number): void => {
    pending.delete(id);
    failed++;
    c.log(`  ... ${label} op ${id} -> HTTP ${status}, counted as failed`);
  };
  while (pending.size && Date.now() - start < maxMs) {
    // Cycle backoff: default 5s; any 429 in the cycle raises it to the longest Retry-After seen
    // (floor 10s, ceiling 60s) so a rate-limited API gets room to recover.
    let backoffMs = POLL_CYCLE_MS;
    await pool([...pending], c.maxParallelRetains, async (id) => {
      try {
        const r = await c.fetchWithAuth(c.bankUrl(`/operations/${id}`), { method: "GET" });
        if (r.status === 429) {
          backoffMs = Math.min(
            RETRY_AFTER_CEILING_MS,
            Math.max(backoffMs, RETRY_AFTER_FLOOR_MS, retryAfterHeaderValue(r))
          );
          return; // op stays pending — retried after the backoff
        }
        if (r.status >= 500) {
          const n = (serverErrors.get(id) ?? 0) + 1;
          serverErrors.set(id, n);
          if (n >= POLL_MAX_SERVER_ERRORS) giveUp(id, r.status);
          return;
        }
        if (!r.ok) return giveUp(id, r.status);
        serverErrors.delete(id);
        const st = (((await r.json()) as { status?: string }).status || "").toLowerCase();
        if (TERMINAL.has(st)) {
          pending.delete(id);
          if (st !== "completed") failed++;
        }
      } catch {
        /* transient — retry next cycle */
      }
    });
    if (pending.size) {
      c.log(`  ... ${pending.size}/${ids.length} ${label} ops pending`);
      await sleep(backoffMs);
    }
  }
  c.log(
    `[wait] ${label} drained — ${ids.length - pending.size} done, ${failed} failed` +
      (pending.size ? `, ${pending.size} still pending at timeout` : "")
  );
}

function retryAfterHeaderValue(r: Response): number {
  return retryAfterMs(r.headers.get("retry-after"));
}
