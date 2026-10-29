import { type HookConnection, resolveHookConnection } from "./hook-connection";

/**
 * The memory-service client the packaged hooks use.
 *
 * It resolves the connection the installer persisted and issues the retrieval
 * and capture calls the hook stages need, over HTTP against the service's
 * existing endpoints. It changes no route, status code or JSON field name: it
 * is a client of the surface the service already serves.
 *
 * A failure never throws: an unreachable service, a non-success status or a
 * body that will not read all become a failure result, so every caller can
 * fail open and let the host continue.
 */

/** The versioned bank-scoped prefix every call is made under. */
export const BANK_PATH_PREFIX = "/v1/default/banks";

/**
 * One document the stop stage writes back.
 *
 * The optional members are the service's own retain parameters, and each one
 * changes what ends up stored: `document_id` is what makes a later write
 * address the same document instead of minting a second one, `update_mode`
 * decides whether that write appends to it or replaces it, and `context` is
 * what extraction reads to tell a speaker's claim from an established fact.
 */
export interface MemoryItem {
  content: string;
  document_id?: string;
  update_mode?: "append" | "replace";
  context?: string;
  tags?: string[];
  metadata?: Record<string, string>;
  timestamp?: string;
}

export interface RecallOutcome {
  ok: boolean;
  /** The retrieved texts, empty on failure or when nothing was relevant. */
  texts: string[];
  reason?: string;
}

export interface CaptureOutcome {
  ok: boolean;
  reason?: string;
}

export interface MemoryClientOptions {
  connection: HookConnection;
  /** The request timeout in milliseconds; the caller bounds it to its budget. */
  timeoutMs: number;
  /** Injectable for tests; defaults to the runtime's fetch. */
  fetchImpl?: typeof fetch;
}

/** One resolved client bound to the connection the installer persisted. */
export class MemoryClient {
  private readonly base: string;
  private readonly bankId: string;
  private readonly token: string | undefined;
  private readonly timeoutMs: number;
  private readonly fetchImpl: typeof fetch;

  constructor(options: MemoryClientOptions) {
    this.base = options.connection.apiUrl.replace(/\/+$/, "");
    this.bankId = options.connection.bankId;
    this.token = options.connection.apiToken;
    this.timeoutMs = options.timeoutMs;
    this.fetchImpl = options.fetchImpl ?? fetch;
  }

  /** The bank-scoped path one call is made against. */
  bankPath(suffix: string): string {
    return `${this.base}${BANK_PATH_PREFIX}/${encodeURIComponent(this.bankId)}${suffix}`;
  }

  /** Retrieves the memories relevant to a query, failing quietly on any error. */
  async recall(query: string): Promise<RecallOutcome> {
    const response = await this.post("/memories/recall", { query });
    if (!response.ok) {
      return { ok: false, texts: [], reason: response.reason };
    }
    const texts: string[] = textsFromRecall(response.body);
    return { ok: true, texts };
  }

  /**
   * Captures one item into memory, failing quietly on any error.
   *
   * Only the members the caller set are sent: an explicit `document_id` of
   * undefined would be a different request from an omitted one, and the service
   * treats a stated document id as addressing a specific document.
   */
  async capture(item: MemoryItem): Promise<CaptureOutcome> {
    const response = await this.post("/memories", {
      items: [item],
      async: false,
    });
    return response.ok ? { ok: true } : { ok: false, reason: response.reason };
  }

  private async post(
    suffix: string,
    body: unknown,
  ): Promise<{ ok: true; body: unknown } | { ok: false; reason: string }> {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeoutMs);
    try {
      const headers: Record<string, string> = { "content-type": "application/json" };
      if (this.token) {
        headers.authorization = `Bearer ${this.token}`;
      }
      const response = await this.fetchImpl(this.bankPath(suffix), {
        method: "POST",
        headers,
        body: JSON.stringify(body),
        signal: controller.signal,
      });
      if (!response.ok) {
        return { ok: false, reason: `the service answered ${response.status}` };
      }
      const parsed: unknown = await response.json().catch(() => null);
      return { ok: true, body: parsed };
    } catch (cause) {
      return { ok: false, reason: cause instanceof Error ? cause.message : "the service was unreachable" };
    } finally {
      clearTimeout(timer);
    }
  }
}

/** Extracts the retrieved texts from a recall response body, tolerating any shape. */
export function textsFromRecall(body: unknown): string[] {
  if (body === null || typeof body !== "object" || Array.isArray(body)) {
    return [];
  }
  const results: unknown = (body as Record<string, unknown>)["results"];
  if (!Array.isArray(results)) {
    return [];
  }
  const texts: string[] = [];
  for (const item of results) {
    if (item !== null && typeof item === "object" && !Array.isArray(item)) {
      const text: unknown = (item as Record<string, unknown>)["text"];
      if (typeof text === "string" && text.length > 0) {
        texts.push(text);
      }
    }
  }
  return texts;
}

/**
 * Resolves the connection and builds a client, or null when no usable
 * connection is configured. The hook then proceeds fail-open.
 */
export async function openMemoryClient(
  timeoutMs: number,
  environment: NodeJS.ProcessEnv = process.env,
): Promise<MemoryClient | null> {
  const resolved = await resolveHookConnection(environment);
  if (!resolved.ok) {
    return null;
  }
  return new MemoryClient({ connection: resolved.connection, timeoutMs });
}