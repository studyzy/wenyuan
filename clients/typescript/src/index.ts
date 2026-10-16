/**
 * WenYuan TypeScript client.
 *
 * A thin façade over the generated SDK: `createClient()` builds a configured
 * client (base URL from the environment, bearer key from the environment) and
 * `retain` / `retainBatch` / `retainFiles` / `recall` / `reflect` are the
 * convenience helpers for the core memory operations. Everything the generator
 * produced is re-exported, so the full 99-operation surface is reachable through
 * this same entry point.
 *
 * ```ts
 * import { createClient, recall, reflect, retain } from "wenyuan-client";
 *
 * const client = createClient(); // reads HINDSIGHT_API_URL / HINDSIGHT_API_KEY
 * await retain(client, "alice", "Alice works at the observatory.");
 * const hits = await recall(client, "alice", "Where does Alice work?");
 * const answer = await reflect(client, "alice", "Summarise Alice's work.");
 * ```
 */

import {
  createClient as createGeneratedClient,
  createConfig,
  mergeHeaders,
} from "../generated/client";
import type {
  Client,
  Config,
  RequestResult,
  ResponseStyle,
} from "../generated/client";
import * as sdk from "../generated/sdk.gen";
import type {
  FileRetainResponse,
  MemoryItem,
  RecallRequest,
  RecallResponse,
  ReflectRequest,
  ReflectResponse,
  RetainRequest,
  RetainResponse,
} from "../generated/types.gen";

// The whole generated surface: every operation and every schema type.
export * from "../generated/sdk.gen";
export * as sdk from "../generated/sdk.gen";
export * from "../generated/types.gen";
export { createConfig, mergeHeaders } from "../generated/client";
export type {
  Client,
  ClientOptions as GeneratedClientOptions,
  Config,
  RequestResult,
  ResponseStyle,
} from "../generated/client";

/** Environment variable holding the API base URL. */
export const ENV_BASE_URL = "HINDSIGHT_API_URL";

/** Environment variable holding the bearer key, when the deployment requires one. */
export const ENV_API_KEY = "HINDSIGHT_API_KEY";

/** Base URL used when neither an option nor {@link ENV_BASE_URL} is set. */
export const DEFAULT_BASE_URL = "http://localhost:18888";

/** Package name, sent as the `User-Agent` prefix. */
export const CLIENT_NAME = "wenyuan-client";

/** Client version; kept in step with `package.json`. */
export const CLIENT_VERSION = "0.1.0";

/** Default `User-Agent` for requests made by this client. */
export const DEFAULT_USER_AGENT = `${CLIENT_NAME}/${CLIENT_VERSION}`;

/** Attempts an idempotent call makes in total, including the first. */
export const DEFAULT_MAX_ATTEMPTS = 3;

/** Fallback backoff, in milliseconds, when the server sends no usable `Retry-After`. */
const FALLBACK_BACKOFF_MS = 500;

/**
 * Parse a `Retry-After` header into milliseconds, if it is usable.
 *
 * Only the delta-seconds form is honoured. The HTTP-date form is legal but the
 * server never sends it, and the caller's own exponential backoff is a better
 * answer than parsing a date; an unusable header reads as absent so the caller
 * falls back rather than waiting out a bogus value.
 */
export function retryAfterMs(response: Response | undefined): number | null {
  const raw = response?.headers?.get("retry-after");
  if (raw === null || raw === undefined) return null;
  const seconds = Number(raw);
  if (!Number.isFinite(seconds)) return null;
  return Math.max(0, seconds * 1000);
}

/**
 * Run `send`, retrying while the server reports it is at capacity (429/503).
 *
 * Intended for **idempotent** operations. Recall and reflect are reads, so a
 * repeat is harmless; writes are deliberately excluded, because a repeated
 * synchronous retain could duplicate a memory.
 *
 * Two details matter beyond the retry itself. `Retry-After` is honoured, because
 * the server sends it precisely because it knows how long its own queue is. And
 * the wait is **jittered**: a burst of clients that all receive `Retry-After: 1`
 * and obey it exactly would return in lockstep and rebuild the spike that caused
 * the rejection, so the sleep is drawn uniformly from `[0, wait]`.
 */
export async function retryOnCapacity<T extends { response?: Response }>(
  send: () => Promise<T>,
  maxAttempts: number,
  random: () => number = Math.random,
  signal?: AbortSignal,
): Promise<T> {
  signal?.throwIfAborted();
  let result = await send();
  signal?.throwIfAborted();
  for (let attempt = 1; attempt < maxAttempts; attempt++) {
    const status = result.response?.status;
    if (status !== 429 && status !== 503) return result;
    const wait = retryAfterMs(result.response) ?? FALLBACK_BACKOFF_MS * 2 ** (attempt - 1);
    // Full jitter, so a synchronised burst spreads out. A cancelled caller must
    // neither wait out `Retry-After` nor start another request; the abort
    // listener is cleaned up on either outcome so successful reads do not
    // accumulate listeners.
    await new Promise<void>((resolve, reject) => {
      const onAbort = () => {
        clearTimeout(timer);
        signal?.removeEventListener("abort", onAbort);
        reject(signal?.reason);
      };
      const timer = setTimeout(() => {
        signal?.removeEventListener("abort", onAbort);
        resolve();
      }, random() * wait);
      signal?.addEventListener("abort", onAbort, { once: true });
      if (signal?.aborted) onAbort();
    });
    signal?.throwIfAborted();
    result = await send();
    signal?.throwIfAborted();
  }
  return result;
}

/** A memory to retain: bare text, or a full item (timestamp, tags, blocks, ...). */
export type RetainContent = string | MemoryItem;

/** Per-call options shared by every helper: an optional cancellation signal. */
export interface RequestOptions {
  /** Cancels the request, and any wait between capacity retries. */
  signal?: AbortSignal;
}

/** Everything {@link retain} accepts besides the content itself. */
export type RetainOptions = Omit<RetainRequest, "items"> & RequestOptions;

/** Everything {@link recall} accepts besides the query. */
export type RecallOptions = Omit<RecallRequest, "query"> & RequestOptions;

/** Everything {@link reflect} accepts besides the query. */
export type ReflectOptions = Omit<ReflectRequest, "query"> & RequestOptions;

/**
 * Everything {@link retainFiles} accepts besides the files.
 *
 * `filesMetadata` entries line up positionally with the uploaded files; when it
 * is omitted every file inherits the shared {@link RetainFilesOptions.context}.
 */
export interface RetainFilesOptions {
  /** Context applied to every file that has no `filesMetadata` entry of its own. */
  context?: string;
  /** Per-file context, document id, tags, metadata and parser override. */
  filesMetadata?: Array<{
    context?: string;
    document_id?: string;
    tags?: string[];
    metadata?: Record<string, string>;
    parser?: string | string[];
  }>;
  signal?: AbortSignal;
}

/** Options for {@link createClient}. */
export interface WenYuanClientOptions {
  /** API base URL; falls back to {@link ENV_BASE_URL}, then {@link DEFAULT_BASE_URL}. */
  baseUrl?: string;
  /** Bearer key; falls back to {@link ENV_API_KEY}. Omitted when neither is set. */
  apiKey?: string;
  /**
   * Product token placed in front of this client's own in the `User-Agent`
   * header, so an integration can identify itself (e.g. `"acme-app/1.2.0"` is
   * sent as `"acme-app/1.2.0 wenyuan-client/0.1.0"`). Defaults to this client's
   * own token.
   */
  userAgent?: string;
  /** Headers merged into every request. */
  headers?: HeadersInit;
  /**
   * Total attempts for *idempotent* calls (recall, reflect) when the server
   * reports it is at capacity (429/503). 1 disables retrying. Waits honour
   * `Retry-After` and are jittered; writes are never retried.
   */
  maxAttempts?: number;
  /** Custom `fetch`, e.g. a stubbed one in tests. Defaults to the global one. */
  fetch?: typeof fetch;
  /** Extra per-request `RequestInit` fields, without `body`/`headers`/`method`. */
  requestInit?: Omit<RequestInit, "body" | "headers" | "method">;
  /**
   * What the SDK returns. `fields` (the default) yields `{ data, error, response }`;
   * `data` yields the payload alone and throws on a failed request.
   */
  responseStyle?: ResponseStyle;
}

/**
 * `process.env` seen through an optional view: runtimes without Node's `process`
 * (browsers, some workers) simply have no environment to read.
 */
const processHolder: { process?: { env?: Record<string, string | undefined> } } =
  globalThis;

/** Read an environment variable; empty and absent both read as unset. */
function env(name: string): string | undefined {
  const value = processHolder.process?.env?.[name];
  return value === undefined || value === "" ? undefined : value;
}

/** Resolve the base URL: option, then environment, then the default; never trailing `/`. */
export function resolveBaseUrl(baseUrl?: string): string {
  const resolved = baseUrl ?? env(ENV_BASE_URL) ?? DEFAULT_BASE_URL;
  return resolved.replace(/\/+$/, "");
}

/**
 * The retry budget chosen for each client built by {@link createClient}.
 *
 * The helpers take a `Client`, and the generated `Client` type is fixed, so the
 * per-client setting rides alongside it here rather than on the client object.
 */
const maxAttemptsByClient = new WeakMap<Client, number>();

/**
 * Build a client pointed at WenYuan.
 *
 * The base URL comes from `options.baseUrl`, else `HINDSIGHT_API_URL`, else
 * `http://localhost:18888`; the bearer key (used only when present, so an open
 * deployment needs no configuration) comes from `options.apiKey`, else
 * `HINDSIGHT_API_KEY`.
 */
export function createClient(options: WenYuanClientOptions = {}): Client {
  const apiKey = options.apiKey ?? env(ENV_API_KEY);
  const userAgent = options.userAgent
    ? `${options.userAgent} ${DEFAULT_USER_AGENT}`
    : DEFAULT_USER_AGENT;
  const config: Config = {
    ...options.requestInit,
    baseUrl: resolveBaseUrl(options.baseUrl),
    fetch: options.fetch,
    responseStyle: options.responseStyle,
    headers: mergeHeaders(
      options.headers,
      { "User-Agent": userAgent },
      apiKey === undefined ? undefined : { Authorization: `Bearer ${apiKey}` },
    ),
  };
  const client = createGeneratedClient(createConfig(config));
  maxAttemptsByClient.set(client, Math.max(1, options.maxAttempts ?? DEFAULT_MAX_ATTEMPTS));
  return client;
}

/** A request that failed, carrying the status the server answered with. */
export class WenYuanError extends Error {
  /** HTTP status of the failed response, when one was received. */
  readonly statusCode: number;
  /** The parsed error body, when the response carried one. */
  readonly details: unknown;

  constructor(message: string, statusCode: number, details: unknown) {
    super(message);
    this.name = "WenYuanError";
    this.statusCode = statusCode;
    this.details = details;
  }
}

/** The slice of the SDK result object these helpers care about. */
interface ApiResult<T> {
  data?: T;
  error?: unknown;
  response?: Response;
}

/** Await a result, turning a server-reported error into a thrown {@link WenYuanError}. */
async function unwrap<T>(
  label: string,
  result: Promise<ApiResult<T>>,
): Promise<T> {
  const { data, error, response } = await result;
  const status = response?.status ?? 0;
  if (error !== undefined) {
    throw new WenYuanError(`${label} failed with status ${status}`, status, error);
  }
  if (data === undefined) {
    throw new WenYuanError(
      `${label} returned no data with status ${status}`,
      status,
      undefined,
    );
  }
  return data;
}

/** Normalise the accepted content forms into memory items. */
function toItems(content: RetainContent | RetainContent[]): MemoryItem[] {
  const list = Array.isArray(content) ? content : [content];
  if (list.length === 0) {
    throw new TypeError("retain needs at least one item");
  }
  return list.map((item) =>
    typeof item === "string" ? { content: item } : item,
  );
}

/**
 * Store one or more memories in a bank.
 *
 * Synchronous by default; pass `{ async: true }` to hand the work to the
 * server's queue and get an operation back immediately.
 */
export function retain(
  client: Client,
  bankId: string,
  content: RetainContent | RetainContent[],
  options: RetainOptions = {},
): Promise<RetainResponse> {
  const { signal, ...body } = options;
  return unwrap(
    "retain",
    sdk.retainMemories({
      client,
      path: { bank_id: bankId },
      body: { ...body, items: toItems(content) },
      signal,
    }),
  );
}

/**
 * Store many memories in a single call.
 *
 * The batch counterpart of {@link retain}: one request carries the whole list,
 * so the server chunks and extracts them together. Each item may carry its own
 * `tags`, `timestamp`, `document_id` and so on; `options` applies to the batch
 * as a whole (`async`, `document_tags`, `operation_id`).
 */
export function retainBatch(
  client: Client,
  bankId: string,
  items: RetainContent[],
  options: RetainOptions = {},
): Promise<RetainResponse> {
  return retain(client, bankId, items, options);
}

/**
 * Upload files and retain their contents as memories.
 *
 * The server converts each file (PDF, DOCX, images via OCR, audio via
 * transcription, ...) to text and ingests it as a document. This is always
 * asynchronous: the response carries one operation id per file, which can be
 * followed through the operations endpoint.
 */
export function retainFiles(
  client: Client,
  bankId: string,
  files: Array<Blob | File>,
  options: RetainFilesOptions = {},
): Promise<FileRetainResponse> {
  if (files.length === 0) {
    throw new TypeError("retainFiles needs at least one file");
  }
  const filesMetadata =
    options.filesMetadata ??
    files.map(() => (options.context === undefined ? {} : { context: options.context }));
  return unwrap(
    "retainFiles",
    sdk.fileRetain({
      client,
      path: { bank_id: bankId },
      body: {
        files,
        request: JSON.stringify({ files_metadata: filesMetadata }),
      },
      signal: options.signal,
    }),
  );
}

/**
 * Search a bank's memories for a query.
 *
 * Retries transient capacity rejections (429/503) up to the client's
 * `maxAttempts`, since a read is safe to repeat.
 */
export function recall(
  client: Client,
  bankId: string,
  query: string,
  options: RecallOptions = {},
): Promise<RecallResponse> {
  const { signal, ...body } = options;
  return unwrap(
    "recall",
    retryOnCapacity(
      () =>
        sdk.recallMemories({
          client,
          path: { bank_id: bankId },
          body: { ...body, query },
          signal,
        }),
      maxAttemptsByClient.get(client) ?? DEFAULT_MAX_ATTEMPTS,
      Math.random,
      signal,
    ),
  );
}

/**
 * Synthesise an answer to a query from a bank's memories.
 *
 * Retries transient capacity rejections (429/503) up to the client's
 * `maxAttempts`, since a read is safe to repeat.
 */
export function reflect(
  client: Client,
  bankId: string,
  query: string,
  options: ReflectOptions = {},
): Promise<ReflectResponse> {
  const { signal, ...body } = options;
  return unwrap(
    "reflect",
    retryOnCapacity(
      () =>
        sdk.reflect({
          client,
          path: { bank_id: bankId },
          body: { ...body, query },
          signal,
        }),
      maxAttemptsByClient.get(client) ?? DEFAULT_MAX_ATTEMPTS,
      Math.random,
      signal,
    ),
  );
}

/**
 * Serialise a recall response to a string suitable for an LLM prompt.
 *
 * Renders the recalled facts as a `FACTS:` JSON section (each result carrying
 * its text, context, temporal fields and the source chunk's text when the
 * response included the chunk), followed by an `ENTITIES:` section built from
 * the entities the response carried. Empty sections are omitted.
 */
export function recallResponseToPromptString(response: RecallResponse): string {
  const chunks = response.chunks ?? {};
  const sections: string[] = [];

  const facts = (response.results ?? []).map((result) => {
    const obj: Record<string, string> = { text: result.text };
    if (result.context) obj.context = result.context;
    if (result.occurred_start) obj.occurred_start = result.occurred_start;
    if (result.occurred_end) obj.occurred_end = result.occurred_end;
    if (result.mentioned_at) obj.mentioned_at = result.mentioned_at;
    const chunk = result.chunk_id ? chunks[result.chunk_id] : undefined;
    if (chunk) obj.source_chunk = chunk.text;
    return obj;
  });
  sections.push("FACTS:\n" + JSON.stringify(facts, null, 2));

  const entities = response.entities;
  if (entities) {
    const parts: string[] = [];
    for (const [name, state] of Object.entries(entities)) {
      const first = state.observations?.[0];
      if (first) parts.push(`## ${name}\n${first.text}`);
    }
    if (parts.length) {
      sections.push("ENTITIES:\n" + parts.join("\n\n"));
    }
  }

  return sections.join("\n\n");
}