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
 * const client = createClient(); // reads WENYUAN_API_URL / WENYUAN_API_KEY
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
  AsyncOperationSubmitResponse,
  BankConfigResponse,
  BankProfileResponse,
  Budget,
  CreateKnowledgePageResponse,
  CreateMentalModelResponse,
  DirectiveListResponse,
  DirectiveResponse,
  DocumentResponse,
  EntityInput,
  FileContentBlock,
  FileRetainResponse,
  ImageContentBlock,
  KnowledgeNode,
  KnowledgePageBundleResponse,
  KnowledgePageResponse,
  KnowledgePageSearchResponse,
  KnowledgeTreeResponse,
  LabelGroupInput,
  ListDocumentsResponse,
  ListMemoryUnitsResponse,
  MemoryItem,
  MentalModelDryRunRefreshResult,
  MentalModelListResponse,
  MentalModelResponse,
  MentalModelTriggerInput,
  MinScores,
  OperationStatusResponse,
  RecallRequest,
  RecallResponse,
  ReflectRequest,
  ReflectResponse,
  RetainRequest,
  RetainResponse,
  TagGroupAndInput,
  TagGroupLeaf,
  TagGroupNotInput,
  TagGroupOrInput,
  TemporalWindow,
  TextContentBlock,
  UpdateDocumentResponse,
  VersionResponse,
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
export const ENV_BASE_URL = "WENYUAN_API_URL";

/** Environment variable holding the bearer key, when the deployment requires one. */
export const ENV_API_KEY = "WENYUAN_API_KEY";

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
 * The base URL comes from `options.baseUrl`, else `WENYUAN_API_URL`, else
 * `http://localhost:18888`; the bearer key (used only when present, so an open
 * deployment needs no configuration) comes from `options.apiKey`, else
 * `WENYUAN_API_KEY`.
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

/* -------------------------------------------------------------------------- *
 * The convenience client
 *
 * The free helpers above take a generated `Client`; this class wraps one and
 * adds one method per memory-bank operation, taking camelCase options and
 * mapping them onto the request the API reads. Both styles run over the same
 * generated SDK, so either can be used and they compose.
 * -------------------------------------------------------------------------- */

/**
 * A memory's content: plain text, or an ordered list of blocks (text, image,
 * file) with each attachment sitting where the caller wrote it.
 */
export type ContentBlock = TextContentBlock | ImageContentBlock | FileContentBlock;

/**
 * A memory to store through {@link WenYuanClient}, in the API's own field names.
 *
 * `timestamp` accepts a `Date` as well as an ISO string; a `Date` is serialised
 * on the way out.
 */
export interface MemoryItemInput {
  content: string | ContentBlock[];
  timestamp?: string | Date;
  context?: string;
  metadata?: Record<string, string>;
  document_id?: string;
  entities?: EntityInput[];
  /** Resolve the supplied `entities` against existing ones (default true); false stores them as written. */
  resolve_entities?: boolean;
  tags?: string[];
  observation_scopes?: "per_tag" | "combined" | "all_combinations" | "shared" | string[][];
  strategy?: string;
  update_mode?: "replace" | "append";
}

/**
 * How a mental model or knowledge page refreshes, in camelCase.
 *
 * One shape for every method that takes a trigger, so a setting exposed on
 * creation is also settable on update.
 */
export interface MentalModelTriggerOptions {
  /** `full` regenerates the content from scratch on each refresh; `delta` edits the existing content in place. */
  mode?: "full" | "delta";
  refreshAfterConsolidation?: boolean;
  /** Cron expression (UTC, 5-field). Mutually exclusive with refreshAfterConsolidation; null removes a schedule. */
  refreshCron?: string | null;
  /** Floor, in seconds, on how often an automatic refresh of this model may run. 0 disables the floor for this model; omit to inherit the bank/global default. */
  minRefreshIntervalSeconds?: number;
  /** Which fact types refresh retrieves. Omit for all of them. */
  factTypes?: Array<"world" | "experience" | "observation">;
  /** Skip the search_mental_models tool during refresh, so this model does not reflect over its siblings. */
  excludeMentalModels?: boolean;
  excludeMentalModelIds?: string[];
  /** How this model's tags filter source memories on refresh. Omitted, a tagged model defaults to `all_strict`. */
  tagsMatch?: "any" | "all" | "any_strict" | "all_strict" | "exact";
  /** Compound tag filter using boolean groups; overrides the model's flat tags/tagsMatch during refresh. */
  tagGroups?: Array<TagGroupLeaf | TagGroupAndInput | TagGroupOrInput | TagGroupNotInput>;
  includeChunks?: boolean;
  recallMaxTokens?: number;
  recallChunksMaxTokens?: number;
  /** Token budget for the refresh's search_observations calls. Omit for the shipped default. */
  reflectSearchObservationsMaxTokens?: number;
  /** Whether search_observations attaches resolved entity names. Omit for enabled. */
  reflectSearchObservationsIncludeEntities?: boolean;
  /** How many agent steps a refresh may spend, as a multiple of the server's reflect iteration limit. Omit for `mid`. */
  budget?: "low" | "mid" | "high";
  /** JSON Schema for structured output, stored alongside the markdown content. */
  responseSchema?: Record<string, unknown>;
  /** Record how each refresh reached its result under reflect_response.trace. */
  keepTrace?: boolean;
}

/**
 * Map the camelCase trigger options onto the snake_case request body.
 *
 * Every key is emitted, and the ones the caller omitted are `undefined`, which
 * JSON serialisation drops. That is what keeps a partial trigger partial: the
 * server patches a supplied trigger over the stored one and reads "named" from
 * the fields the request actually carried, so a mapper that filled in its own
 * defaults would silently reset the settings the caller never mentioned.
 */
function toTriggerBody(trigger: MentalModelTriggerOptions): MentalModelTriggerInput {
  return {
    mode: trigger.mode,
    refresh_after_consolidation: trigger.refreshAfterConsolidation,
    refresh_cron: trigger.refreshCron,
    min_refresh_interval_seconds: trigger.minRefreshIntervalSeconds,
    fact_types: trigger.factTypes,
    exclude_mental_models: trigger.excludeMentalModels,
    exclude_mental_model_ids: trigger.excludeMentalModelIds,
    tags_match: trigger.tagsMatch,
    tag_groups: trigger.tagGroups,
    include_chunks: trigger.includeChunks,
    recall_max_tokens: trigger.recallMaxTokens,
    recall_chunks_max_tokens: trigger.recallChunksMaxTokens,
    reflect_search_observations_max_tokens: trigger.reflectSearchObservationsMaxTokens,
    reflect_search_observations_include_entities: trigger.reflectSearchObservationsIncludeEntities,
    budget: trigger.budget,
    response_schema: trigger.responseSchema,
    keep_trace: trigger.keepTrace,
  };
}

/**
 * Warn when a caller-supplied `operationId` will be silently ignored.
 *
 * An `operationId` only enables idempotent retries for an asynchronous retain;
 * on a synchronous request it is dropped before reaching the API, so surface
 * the likely mistake rather than failing silently.
 */
function warnIfOperationIdDropped(
  async: boolean | undefined,
  operationId: string | null | undefined,
): void {
  if (operationId != null && async !== true) {
    console.warn(
      "operationId is ignored for synchronous retain; pass async: true to enable idempotent retries.",
    );
  }
}

/**
 * Tag filter for the knowledge-base tree and search, with recall's semantics.
 */
export interface KnowledgeTagFilterOptions {
  tags?: string[];
  tagsMatch?: "any" | "all" | "any_strict" | "all_strict" | "exact";
  tagGroups?: Array<TagGroupLeaf | TagGroupAndInput | TagGroupOrInput | TagGroupNotInput>;
}

/** Map the camelCase tag filter onto the query a GET carries. */
function knowledgeTagQuery(options?: KnowledgeTagFilterOptions) {
  return {
    ...(options?.tags?.length ? { tags: options.tags } : {}),
    ...(options?.tagsMatch ? { tags_match: options.tagsMatch } : {}),
    // A GET carries the boolean tree as one JSON-encoded query parameter.
    ...(options?.tagGroups?.length ? { tag_groups: JSON.stringify(options.tagGroups) } : {}),
  };
}

/**
 * One method per memory-bank operation, with camelCase options.
 *
 * Construct it with the same options {@link createClient} takes (the base URL
 * and key fall back to the environment the same way) and call the operation you
 * want:
 *
 * ```ts
 * const client = new WenYuanClient();
 * await client.retain("alice", "Alice works at the observatory.");
 * const hits = await client.recall("alice", "Where does Alice work?");
 * ```
 *
 * Every method resolves with the decoded payload and throws a
 * {@link WenYuanError} (carrying `statusCode` and `details`) when the server
 * answers with an error status.
 */
export class WenYuanClient {
  private client: Client;
  private maxAttempts: number;

  constructor(options: WenYuanClientOptions = {}) {
    // The class reads the `{ data, error, response }` result itself, so it
    // always asks the SDK for that shape regardless of what the caller set.
    this.client = createClient({ ...options, responseStyle: undefined });
    this.maxAttempts = Math.max(1, options.maxAttempts ?? DEFAULT_MAX_ATTEMPTS);
  }

  /** Turn an SDK result into the payload, or throw the server's error. */
  private validateResponse<T>(response: ApiResult<T>, operation: string): T {
    if (!response.data) {
      // The generated client returns { error, response }; a status code, when
      // there was a response at all, lives on it rather than on the error.
      const error = response.error as { detail?: unknown; message?: unknown } | undefined;
      const statusCode = response.response?.status ?? 0;
      const details = error?.detail ?? error?.message ?? error;

      throw new WenYuanError(
        `${operation} failed: ${JSON.stringify(details)}`,
        statusCode,
        details,
      );
    }
    return response.data;
  }

  /** Get the API version and feature flags for the connected deployment. */
  async getVersion(options?: { signal?: AbortSignal }): Promise<VersionResponse> {
    const response = await sdk.getVersion({
      client: this.client,
      signal: options?.signal,
    });

    return this.validateResponse(response, "getVersion");
  }

  /** Retain a single memory for a bank. */
  async retain(
    bankId: string,
    content: string | ContentBlock[],
    options?: {
      timestamp?: Date | string;
      context?: string;
      metadata?: Record<string, string>;
      documentId?: string;
      async?: boolean;
      /** Optional caller-supplied UUID for idempotent async retries. */
      operationId?: string;
      entities?: EntityInput[];
      /** Resolve the supplied `entities` against existing ones (default true); false stores them as written. */
      resolveEntities?: boolean;
      /** Optional list of tags for this memory. */
      tags?: string[];
      /** How to handle existing documents: 'replace' (default) or 'append'. */
      updateMode?: "replace" | "append";
      /** Observation scoping strategy, or explicit scope groups. */
      observationScopes?: "per_tag" | "combined" | "all_combinations" | "shared" | string[][];
      /** Extraction strategy override. */
      strategy?: string;
      signal?: AbortSignal;
    },
  ): Promise<RetainResponse> {
    warnIfOperationIdDropped(options?.async, options?.operationId);
    return this.retainBatch(
      bankId,
      [
        {
          content,
          timestamp: options?.timestamp,
          context: options?.context,
          metadata: options?.metadata,
          document_id: options?.documentId,
          entities: options?.entities,
          resolve_entities: options?.resolveEntities,
          tags: options?.tags,
          update_mode: options?.updateMode,
          observation_scopes: options?.observationScopes,
          strategy: options?.strategy,
        },
      ],
      {
        async: options?.async,
        signal: options?.signal,
        ...(options?.async === true && options.operationId != null
          ? { operationId: options.operationId }
          : {}),
      },
    );
  }

  /** Retain multiple memories in one batch. */
  async retainBatch(
    bankId: string,
    items: MemoryItemInput[],
    options?: {
      documentId?: string;
      documentTags?: string[];
      async?: boolean;
      /** Optional caller-supplied UUID for idempotent async retries. */
      operationId?: string;
      signal?: AbortSignal;
    },
  ): Promise<RetainResponse> {
    warnIfOperationIdDropped(options?.async, options?.operationId);
    const processedItems = items.map((item) => ({
      content: item.content,
      context: item.context,
      metadata: item.metadata,
      document_id: item.document_id,
      entities: item.entities,
      resolve_entities: item.resolve_entities,
      tags: item.tags,
      observation_scopes: item.observation_scopes,
      strategy: item.strategy,
      update_mode: item.update_mode,
      timestamp: item.timestamp instanceof Date ? item.timestamp.toISOString() : item.timestamp,
    }));

    // A batch-level documentId applies to every item that names none of its own.
    const itemsWithDocId = processedItems.map((item) => ({
      ...item,
      document_id: item.document_id || options?.documentId,
    }));

    const response = await sdk.retainMemories({
      client: this.client,
      path: { bank_id: bankId },
      body: {
        items: itemsWithDocId,
        document_tags: options?.documentTags,
        async: options?.async,
        ...(options?.async === true && options.operationId != null
          ? { operation_id: options.operationId }
          : {}),
      },
      signal: options?.signal,
    });

    return this.validateResponse(response, "retainBatch");
  }

  /**
   * Upload files and retain their contents as memories.
   *
   * Always asynchronous: the response carries one operation id per file.
   */
  async retainFiles(
    bankId: string,
    files: Array<File | Blob>,
    options?: {
      context?: string;
      filesMetadata?: Array<{
        context?: string;
        document_id?: string;
        tags?: string[];
        metadata?: Record<string, string>;
      }>;
      signal?: AbortSignal;
    },
  ): Promise<FileRetainResponse> {
    const meta =
      options?.filesMetadata ??
      files.map(() => (options?.context ? { context: options.context } : {}));

    const response = await sdk.fileRetain({
      client: this.client,
      path: { bank_id: bankId },
      body: { files, request: JSON.stringify({ files_metadata: meta }) },
      signal: options?.signal,
    });

    return this.validateResponse(response, "retainFiles");
  }

  /** Recall memories with a natural-language query. */
  async recall(
    bankId: string,
    query: string,
    options?: {
      types?: string[];
      /** Drop raw facts a returned observation was consolidated from, so the observation supersedes them. */
      preferObservations?: boolean;
      maxTokens?: number;
      budget?: Budget;
      trace?: boolean;
      queryTimestamp?: string;
      includeEntities?: boolean;
      maxEntityTokens?: number;
      includeChunks?: boolean;
      maxChunkTokens?: number;
      /** Include source facts for observation-type results. */
      includeSourceFacts?: boolean;
      /** Maximum tokens for source facts (default: 4096). */
      maxSourceFactsTokens?: number;
      /** Optional list of tags to filter memories by. */
      tags?: string[];
      /** How to match tags: 'any' (OR, includes untagged), 'all' (AND, includes untagged), 'any_strict' (OR, excludes untagged), 'all_strict' (AND, excludes untagged), 'exact' (set equality, excludes untagged). Default: 'any'. */
      tagsMatch?: "any" | "all" | "any_strict" | "all_strict" | "exact";
      /** Compound tag filter using boolean groups; mutually exclusive with tags/tagsMatch. */
      tagGroups?: Array<TagGroupLeaf | TagGroupAndInput | TagGroupOrInput | TagGroupNotInput>;
      /** Optional per-stage score floors, e.g. `{semantic: 0.2, final: 0.5}`. */
      minScores?: MinScores;
      /** Window for the temporal retrieval arm, supplied instead of extracting dates from the query text. */
      temporalWindow?: TemporalWindow;
      signal?: AbortSignal;
    },
  ): Promise<RecallResponse> {
    const response = await retryOnCapacity(
      () =>
        sdk.recallMemories({
          client: this.client,
          path: { bank_id: bankId },
          body: {
            query,
            types: options?.types,
            prefer_observations: options?.preferObservations,
            max_tokens: options?.maxTokens,
            budget: options?.budget || "mid",
            trace: options?.trace,
            query_timestamp: options?.queryTimestamp,
            include: {
              entities:
                options?.includeEntities === false
                  ? null
                  : options?.includeEntities
                    ? { max_tokens: options?.maxEntityTokens ?? 500 }
                    : undefined,
              chunks: options?.includeChunks
                ? { max_tokens: options?.maxChunkTokens ?? 8192 }
                : undefined,
              source_facts: options?.includeSourceFacts
                ? { max_tokens: options?.maxSourceFactsTokens ?? 4096 }
                : undefined,
            },
            tags: options?.tags,
            tags_match: options?.tagsMatch,
            tag_groups: options?.tagGroups,
            min_scores: options?.minScores,
            temporal_window: options?.temporalWindow,
          },
          signal: options?.signal,
        }),
      this.maxAttempts,
      Math.random,
      options?.signal,
    );

    return this.validateResponse(response, "recall");
  }

  /** Reflect over a bank's memories and generate a contextual answer. */
  async reflect(
    bankId: string,
    query: string,
    options?: {
      context?: string;
      budget?: Budget;
      /** Optional list of tags to filter memories by. */
      tags?: string[];
      /** How to match tags: 'any', 'all', 'any_strict', 'all_strict' or 'exact'. Default: 'any'. */
      tagsMatch?: "any" | "all" | "any_strict" | "all_strict" | "exact";
      /** Compound tag filter using boolean groups; mutually exclusive with tags/tagsMatch. */
      tagGroups?: Array<TagGroupLeaf | TagGroupAndInput | TagGroupOrInput | TagGroupNotInput>;
      /** Apply every active directive regardless of tags. */
      applyAllDirectives?: boolean;
      /** Optional JSON Schema for structured output. */
      responseSchema?: Record<string, unknown>;
      /** Filter which fact types are retrieved: 'world', 'experience', 'observation'. None means all. */
      factTypes?: Array<"world" | "experience" | "observation">;
      /** If true, exclude all mental models from reflection. */
      excludeMentalModels?: boolean;
      /** Exclude specific mental models by ID from reflection. */
      excludeMentalModelIds?: string[];
      /** Token budget for the agent's search_observations calls. Omit for the bank's default. */
      reflectSearchObservationsMaxTokens?: number;
      /** Whether search_observations attaches resolved entity names. Omit for the bank default. */
      reflectSearchObservationsIncludeEntities?: boolean;
      /** If true, the response includes a 'based_on' field listing what the answer used. */
      includeFacts?: boolean;
      /** If true, the response includes a 'trace' field of the tool and LLM calls made. */
      includeToolCalls?: boolean;
      /** With includeToolCalls, set false for an inputs-only trace. Default: true. */
      includeToolCallOutput?: boolean;
      signal?: AbortSignal;
    },
  ): Promise<ReflectResponse> {
    const include =
      options?.includeFacts || options?.includeToolCalls
        ? {
            facts: options?.includeFacts ? {} : undefined,
            tool_calls: options?.includeToolCalls
              ? { output: options?.includeToolCallOutput ?? true }
              : undefined,
          }
        : undefined;
    const response = await retryOnCapacity(
      () =>
        sdk.reflect({
          client: this.client,
          path: { bank_id: bankId },
          body: {
            query,
            context: options?.context,
            budget: options?.budget || "low",
            tags: options?.tags,
            tags_match: options?.tagsMatch,
            tag_groups: options?.tagGroups,
            apply_all_directives: options?.applyAllDirectives,
            response_schema: options?.responseSchema,
            fact_types: options?.factTypes,
            exclude_mental_models: options?.excludeMentalModels,
            exclude_mental_model_ids: options?.excludeMentalModelIds,
            reflect_search_observations_max_tokens: options?.reflectSearchObservationsMaxTokens,
            reflect_search_observations_include_entities:
              options?.reflectSearchObservationsIncludeEntities,
            include,
          },
          signal: options?.signal,
        }),
      this.maxAttempts,
      Math.random,
      options?.signal,
    );

    return this.validateResponse(response, "reflect");
  }

  /** List memories with pagination and optional filters. */
  async listMemories(
    bankId: string,
    options?: {
      limit?: number;
      offset?: number;
      type?: string;
      q?: string;
      consolidationState?: "failed" | "pending" | "done";
      state?: "valid" | "invalidated";
      documentId?: string;
      entityId?: string;
      /**
       * Time axis to filter and order by. Also drops memories with no value on
       * that column, so `total` counts only the ones inside the window.
       */
      timeField?: "created_at" | "updated_at" | "mentioned_at" | "occurred_start" | "occurred_end";
      /** ISO-8601, inclusive. */
      startDate?: string;
      /** ISO-8601, exclusive. */
      endDate?: string;
      /** Filter by the memories' tags. */
      tags?: string[];
      /** How `tags` match; the server defaults to "any". */
      tagsMatch?: "any" | "all" | "any_strict" | "all_strict" | "exact";
      signal?: AbortSignal;
    },
  ): Promise<ListMemoryUnitsResponse> {
    const response = await sdk.listMemories({
      client: this.client,
      path: { bank_id: bankId },
      query: {
        limit: options?.limit,
        offset: options?.offset,
        type: options?.type,
        q: options?.q,
        consolidation_state: options?.consolidationState,
        state: options?.state,
        document_id: options?.documentId,
        entity_id: options?.entityId,
        time_field: options?.timeField,
        start_date: options?.startDate,
        end_date: options?.endDate,
        tags: options?.tags,
        tags_match: options?.tagsMatch,
      },
      signal: options?.signal,
    });

    return this.validateResponse(response, "listMemories");
  }

  /** Create or update a bank with its identity, missions and operational configuration. */
  async createBank(
    bankId: string,
    options: {
      /** Display label only. */
      name?: string;
      /** Mission/context for reflect operations. */
      reflectMission?: string;
      /** Steers what gets extracted during retain. */
      retainMission?: string;
      /** Fact extraction mode: 'concise' (default), 'verbose', 'custom', 'verbatim', or 'chunks'. */
      retainExtractionMode?: string;
      /** Custom extraction prompt (only active when retainExtractionMode is 'custom'). */
      retainCustomInstructions?: string;
      /** Target maximum characters for each content chunk during retain. */
      retainChunkSize?: number;
      /** Maximum characters for a single JSONL line or conversation turn to keep whole during retain. */
      retainStructuredChunkSize?: number;
      /** Max inline attachments one extraction chunk may carry. */
      retainMaxAttachmentsPerChunk?: number;
      /** Toggle automatic observation consolidation after retain. */
      enableObservations?: boolean;
      /** Controls what gets synthesised into observations. Replaces the built-in rules. */
      observationsMission?: string;
      /** Run the keyword (BM25) retrieval arm during recall. */
      enableTextSearch?: boolean;
      /** Run the temporal retrieval arm during recall. */
      enableTemporalRetrieval?: boolean;
      /** Run the entity/link graph traversal arm during recall. */
      enableGraphRetrieval?: boolean;
      /** Rerank fused candidates with the cross-encoder. */
      enableReranking?: boolean;
      signal?: AbortSignal;
    } = {},
  ): Promise<BankProfileResponse> {
    const response = await sdk.createOrUpdateBank({
      client: this.client,
      path: { bank_id: bankId },
      body: {
        name: options.name,
        reflect_mission: options.reflectMission,
        retain_mission: options.retainMission,
        retain_extraction_mode: options.retainExtractionMode,
        retain_custom_instructions: options.retainCustomInstructions,
        retain_chunk_size: options.retainChunkSize,
        retain_structured_chunk_size: options.retainStructuredChunkSize,
        retain_max_attachments_per_chunk: options.retainMaxAttachmentsPerChunk,
        enable_observations: options.enableObservations,
        observations_mission: options.observationsMission,
        enable_text_search: options.enableTextSearch,
        enable_temporal_retrieval: options.enableTemporalRetrieval,
        enable_graph_retrieval: options.enableGraphRetrieval,
        enable_reranking: options.enableReranking,
      },
      signal: options.signal,
    });

    return this.validateResponse(response, "createBank");
  }

  /**
   * Set or update the reflect mission for a bank.
   *
   * The name is kept because it is part of this client's published surface;
   * `createBank(bankId, { reflectMission })` is the same call with the whole
   * bank body available.
   */
  async setMission(
    bankId: string,
    mission: string,
    options?: { signal?: AbortSignal },
  ): Promise<BankProfileResponse> {
    return this.createBank(bankId, { reflectMission: mission, signal: options?.signal });
  }

  /**
   * Get a bank's profile.
   *
   * The route is retired server-side and answers 410. Disposition traits and the
   * reflect mission are bank configuration: read them from
   * {@link WenYuanClient.getBankConfig} instead.
   */
  async getBankProfile(
    bankId: string,
    options?: { signal?: AbortSignal },
  ): Promise<BankProfileResponse> {
    const response = await sdk.getBankProfile({
      client: this.client,
      path: { bank_id: bankId },
      signal: options?.signal,
    });

    return this.validateResponse(response, "getBankProfile");
  }

  /** Get the resolved configuration for a bank, including any bank-level overrides. */
  async getBankConfig(
    bankId: string,
    options?: { signal?: AbortSignal },
  ): Promise<BankConfigResponse> {
    const response = await sdk.getBankConfig({
      client: this.client,
      path: { bank_id: bankId },
      signal: options?.signal,
    });

    return this.validateResponse(response, "getBankConfig");
  }

  /**
   * Update configuration overrides for a bank.
   *
   * Only the fields the caller named are sent, so a setting left out keeps its
   * current value.
   */
  async updateBankConfig(
    bankId: string,
    options: {
      reflectMission?: string;
      retainMission?: string;
      retainExtractionMode?: string;
      retainCustomInstructions?: string;
      retainChunkSize?: number;
      retainStructuredChunkSize?: number;
      /** Max inline attachments one extraction chunk may carry. */
      retainMaxAttachmentsPerChunk?: number;
      /** Controlled vocabulary for entity labels. */
      entityLabels?: LabelGroupInput[];
      /** Allow entities outside `entityLabels`. False is labels-only mode. */
      entitiesAllowFreeForm?: boolean;
      enableObservations?: boolean;
      observationsMission?: string;
      /** Run the keyword (BM25) retrieval arm during recall. */
      enableTextSearch?: boolean;
      /** Run the temporal retrieval arm during recall. */
      enableTemporalRetrieval?: boolean;
      /** Run the entity/link graph traversal arm during recall. */
      enableGraphRetrieval?: boolean;
      /** Rerank fused candidates with the cross-encoder. */
      enableReranking?: boolean;
      /** How skeptical vs trusting (1=trusting, 5=skeptical). */
      dispositionSkepticism?: number;
      /** How literally to interpret information (1=flexible, 5=literal). */
      dispositionLiteralism?: number;
      /** How much to consider emotional context (1=detached, 5=empathetic). */
      dispositionEmpathy?: number;
      /** Default retain strategy name. */
      retainDefaultStrategy?: string;
      /** Named strategy definitions (strategy name to config). */
      retainStrategies?: Record<string, unknown>;
      /** Number of chunks per sub-batch in chunks extraction mode. */
      retainChunkBatchSize?: number;
      /** Persist the original document text alongside extracted facts. */
      storeDocumentText?: boolean;
      /** Cap on observations retained per scope (-1 for unlimited). */
      maxObservationsPerScope?: number;
      /** Per-scope observation caps, overriding maxObservationsPerScope. */
      observationScopeLimits?: Record<string, unknown>[];
      consolidationStrategies?: Record<string, unknown>[];
      /** Consolidate automatically after retain rather than on demand. */
      enableAutoConsolidation?: boolean;
      /** Number of LLM calls to batch during consolidation. */
      consolidationLlmBatchSize?: number;
      /** Concurrent LLM calls during consolidation. */
      consolidationLlmParallelism?: number;
      /** Memories consolidated per round. */
      consolidationMaxMemoriesPerRound?: number;
      /** Max tokens for source facts across all observations in a pass. */
      consolidationSourceFactsMaxTokens?: number;
      /** Max tokens of source facts per observation in the prompt. */
      consolidationSourceFactsMaxTokensPerObservation?: number;
      /** Debounce between mental-model refreshes. */
      mentalModelMinRefreshIntervalSeconds?: number;
      /** Trigger fields merged over the built-in default for new knowledge pages. */
      knowledgePageDefaultTrigger?: Record<string, unknown>;
      /** Default reflect options for this bank. */
      reflectDefaultOptions?: Record<string, unknown>;
      /** Token budget for source facts during reflect. -1 disables. */
      reflectSourceFactsMaxTokens?: number;
      /** Token budget for facts returned by recall. */
      recallMaxTokens?: number;
      /** Include source chunks in recall results. */
      recallIncludeChunks?: boolean;
      /** Token budget for those chunks. */
      recallChunksMaxTokens?: number;
      /** How the per-query result budget is derived: 'fixed' or 'adaptive'. */
      recallBudgetFunction?: string;
      /** Fixed budget for low-breadth queries. */
      recallBudgetFixedLow?: number;
      /** Fixed budget for medium-breadth queries. */
      recallBudgetFixedMid?: number;
      /** Fixed budget for high-breadth queries. */
      recallBudgetFixedHigh?: number;
      /** Adaptive budget fraction for low-breadth queries. */
      recallBudgetAdaptiveLow?: number;
      /** Adaptive budget fraction for medium-breadth queries. */
      recallBudgetAdaptiveMid?: number;
      /** Adaptive budget fraction for high-breadth queries. */
      recallBudgetAdaptiveHigh?: number;
      /** Lower clamp on the resolved budget. */
      recallBudgetMin?: number;
      /** Upper clamp on the resolved budget. */
      recallBudgetMax?: number;
      /** MCP tool names enabled for this bank. */
      mcpEnabledTools?: string[];
      /** Gemini/VertexAI safety overrides. */
      llmGeminiSafetySettings?: { category: string; threshold: string }[];
      /** Memory-defense (prompt-injection / secret redaction) settings. */
      memoryDefense?: Record<string, unknown>;
      /** Write an audit log entry for each operation on this bank. */
      auditLogEnabled?: boolean;
      signal?: AbortSignal;
    },
  ): Promise<BankConfigResponse> {
    const updates: Record<string, unknown> = {};
    if (options.reflectMission !== undefined) updates.reflect_mission = options.reflectMission;
    if (options.retainMission !== undefined) updates.retain_mission = options.retainMission;
    if (options.retainExtractionMode !== undefined)
      updates.retain_extraction_mode = options.retainExtractionMode;
    if (options.retainCustomInstructions !== undefined)
      updates.retain_custom_instructions = options.retainCustomInstructions;
    if (options.retainChunkSize !== undefined) updates.retain_chunk_size = options.retainChunkSize;
    if (options.retainStructuredChunkSize !== undefined)
      updates.retain_structured_chunk_size = options.retainStructuredChunkSize;
    if (options.retainMaxAttachmentsPerChunk !== undefined)
      updates.retain_max_attachments_per_chunk = options.retainMaxAttachmentsPerChunk;
    if (options.entityLabels !== undefined) updates.entity_labels = options.entityLabels;
    if (options.entitiesAllowFreeForm !== undefined)
      updates.entities_allow_free_form = options.entitiesAllowFreeForm;
    if (options.enableObservations !== undefined)
      updates.enable_observations = options.enableObservations;
    if (options.observationsMission !== undefined)
      updates.observations_mission = options.observationsMission;
    if (options.enableTextSearch !== undefined)
      updates.enable_text_search = options.enableTextSearch;
    if (options.enableTemporalRetrieval !== undefined)
      updates.enable_temporal_retrieval = options.enableTemporalRetrieval;
    if (options.enableGraphRetrieval !== undefined)
      updates.enable_graph_retrieval = options.enableGraphRetrieval;
    if (options.enableReranking !== undefined) updates.enable_reranking = options.enableReranking;
    if (options.dispositionSkepticism !== undefined)
      updates.disposition_skepticism = options.dispositionSkepticism;
    if (options.dispositionLiteralism !== undefined)
      updates.disposition_literalism = options.dispositionLiteralism;
    if (options.dispositionEmpathy !== undefined)
      updates.disposition_empathy = options.dispositionEmpathy;
    if (options.retainDefaultStrategy !== undefined)
      updates.retain_default_strategy = options.retainDefaultStrategy;
    if (options.retainStrategies !== undefined)
      updates.retain_strategies = options.retainStrategies;
    if (options.retainChunkBatchSize !== undefined)
      updates.retain_chunk_batch_size = options.retainChunkBatchSize;
    if (options.storeDocumentText !== undefined)
      updates.store_document_text = options.storeDocumentText;
    if (options.maxObservationsPerScope !== undefined)
      updates.max_observations_per_scope = options.maxObservationsPerScope;
    if (options.observationScopeLimits !== undefined)
      updates.observation_scope_limits = options.observationScopeLimits;
    if (options.consolidationStrategies !== undefined)
      updates.consolidation_strategies = options.consolidationStrategies;
    if (options.enableAutoConsolidation !== undefined)
      updates.enable_auto_consolidation = options.enableAutoConsolidation;
    if (options.consolidationLlmBatchSize !== undefined)
      updates.consolidation_llm_batch_size = options.consolidationLlmBatchSize;
    if (options.consolidationLlmParallelism !== undefined)
      updates.consolidation_llm_parallelism = options.consolidationLlmParallelism;
    if (options.consolidationMaxMemoriesPerRound !== undefined)
      updates.consolidation_max_memories_per_round = options.consolidationMaxMemoriesPerRound;
    if (options.consolidationSourceFactsMaxTokens !== undefined)
      updates.consolidation_source_facts_max_tokens = options.consolidationSourceFactsMaxTokens;
    if (options.consolidationSourceFactsMaxTokensPerObservation !== undefined)
      updates.consolidation_source_facts_max_tokens_per_observation =
        options.consolidationSourceFactsMaxTokensPerObservation;
    if (options.mentalModelMinRefreshIntervalSeconds !== undefined)
      updates.mental_model_min_refresh_interval_seconds =
        options.mentalModelMinRefreshIntervalSeconds;
    if (options.knowledgePageDefaultTrigger !== undefined)
      updates.knowledge_page_default_trigger = options.knowledgePageDefaultTrigger;
    if (options.reflectDefaultOptions !== undefined)
      updates.reflect_default_options = options.reflectDefaultOptions;
    if (options.reflectSourceFactsMaxTokens !== undefined)
      updates.reflect_source_facts_max_tokens = options.reflectSourceFactsMaxTokens;
    if (options.recallMaxTokens !== undefined) updates.recall_max_tokens = options.recallMaxTokens;
    if (options.recallIncludeChunks !== undefined)
      updates.recall_include_chunks = options.recallIncludeChunks;
    if (options.recallChunksMaxTokens !== undefined)
      updates.recall_chunks_max_tokens = options.recallChunksMaxTokens;
    if (options.recallBudgetFunction !== undefined)
      updates.recall_budget_function = options.recallBudgetFunction;
    if (options.recallBudgetFixedLow !== undefined)
      updates.recall_budget_fixed_low = options.recallBudgetFixedLow;
    if (options.recallBudgetFixedMid !== undefined)
      updates.recall_budget_fixed_mid = options.recallBudgetFixedMid;
    if (options.recallBudgetFixedHigh !== undefined)
      updates.recall_budget_fixed_high = options.recallBudgetFixedHigh;
    if (options.recallBudgetAdaptiveLow !== undefined)
      updates.recall_budget_adaptive_low = options.recallBudgetAdaptiveLow;
    if (options.recallBudgetAdaptiveMid !== undefined)
      updates.recall_budget_adaptive_mid = options.recallBudgetAdaptiveMid;
    if (options.recallBudgetAdaptiveHigh !== undefined)
      updates.recall_budget_adaptive_high = options.recallBudgetAdaptiveHigh;
    if (options.recallBudgetMin !== undefined) updates.recall_budget_min = options.recallBudgetMin;
    if (options.recallBudgetMax !== undefined) updates.recall_budget_max = options.recallBudgetMax;
    if (options.mcpEnabledTools !== undefined) updates.mcp_enabled_tools = options.mcpEnabledTools;
    if (options.llmGeminiSafetySettings !== undefined)
      updates.llm_gemini_safety_settings = options.llmGeminiSafetySettings;
    if (options.memoryDefense !== undefined) updates.memory_defense = options.memoryDefense;
    if (options.auditLogEnabled !== undefined) updates.audit_log_enabled = options.auditLogEnabled;

    const response = await sdk.updateBankConfig({
      client: this.client,
      path: { bank_id: bankId },
      body: { updates },
      signal: options.signal,
    });

    return this.validateResponse(response, "updateBankConfig");
  }

  /** Reset all bank-level configuration overrides, reverting to the server defaults. */
  async resetBankConfig(
    bankId: string,
    options?: { signal?: AbortSignal },
  ): Promise<BankConfigResponse> {
    const response = await sdk.resetBankConfig({
      client: this.client,
      path: { bank_id: bankId },
      signal: options?.signal,
    });

    return this.validateResponse(response, "resetBankConfig");
  }

  /** Delete a bank. */
  async deleteBank(bankId: string, options?: { signal?: AbortSignal }): Promise<void> {
    const response = await sdk.deleteBank({
      client: this.client,
      path: { bank_id: bankId },
      signal: options?.signal,
    });
    if (response.error) {
      throw new Error(`deleteBank failed: ${JSON.stringify(response.error)}`);
    }
  }

  /** Create a directive (a hard rule for reflect). */
  async createDirective(
    bankId: string,
    name: string,
    content: string,
    options?: {
      priority?: number;
      isActive?: boolean;
      tags?: string[];
      signal?: AbortSignal;
    },
  ): Promise<DirectiveResponse> {
    const response = await sdk.createDirective({
      client: this.client,
      path: { bank_id: bankId },
      body: {
        name,
        content,
        priority: options?.priority ?? 0,
        is_active: options?.isActive ?? true,
        tags: options?.tags,
      },
      signal: options?.signal,
    });

    return this.validateResponse(response, "createDirective");
  }

  /** List a bank's directives. */
  async listDirectives(
    bankId: string,
    options?: { tags?: string[]; limit?: number; offset?: number; signal?: AbortSignal },
  ): Promise<DirectiveListResponse> {
    const response = await sdk.listDirectives({
      client: this.client,
      path: { bank_id: bankId },
      query: {
        tags: options?.tags,
        ...(options?.limit !== undefined ? { limit: options.limit } : {}),
        ...(options?.offset !== undefined ? { offset: options.offset } : {}),
      },
      signal: options?.signal,
    });

    return this.validateResponse(response, "listDirectives");
  }

  /** Get a specific directive. */
  async getDirective(
    bankId: string,
    directiveId: string,
    options?: { signal?: AbortSignal },
  ): Promise<DirectiveResponse> {
    const response = await sdk.getDirective({
      client: this.client,
      path: { bank_id: bankId, directive_id: directiveId },
      signal: options?.signal,
    });

    return this.validateResponse(response, "getDirective");
  }

  /** Update a directive. */
  async updateDirective(
    bankId: string,
    directiveId: string,
    options: {
      name?: string;
      content?: string;
      priority?: number;
      isActive?: boolean;
      tags?: string[];
      signal?: AbortSignal;
    },
  ): Promise<DirectiveResponse> {
    const response = await sdk.updateDirective({
      client: this.client,
      path: { bank_id: bankId, directive_id: directiveId },
      body: {
        name: options.name,
        content: options.content,
        priority: options.priority,
        is_active: options.isActive,
        tags: options.tags,
      },
      signal: options.signal,
    });

    return this.validateResponse(response, "updateDirective");
  }

  /** Delete a directive. */
  async deleteDirective(
    bankId: string,
    directiveId: string,
    options?: { signal?: AbortSignal },
  ): Promise<void> {
    const response = await sdk.deleteDirective({
      client: this.client,
      path: { bank_id: bankId, directive_id: directiveId },
      signal: options?.signal,
    });
    if (response.error) {
      throw new Error(`deleteDirective failed: ${JSON.stringify(response.error)}`);
    }
  }

  /** Create a mental model (its first build runs in the background). */
  async createMentalModel(
    bankId: string,
    name: string,
    sourceQuery: string,
    options?: {
      id?: string;
      tags?: string[];
      maxTokens?: number;
      trigger?: MentalModelTriggerOptions;
      signal?: AbortSignal;
    },
  ): Promise<CreateMentalModelResponse> {
    const response = await sdk.createMentalModel({
      client: this.client,
      path: { bank_id: bankId },
      body: {
        id: options?.id,
        name,
        source_query: sourceQuery,
        tags: options?.tags,
        max_tokens: options?.maxTokens,
        trigger: options?.trigger ? toTriggerBody(options.trigger) : undefined,
      },
      signal: options?.signal,
    });

    return this.validateResponse(response, "createMentalModel");
  }

  /**
   * List a bank's mental models.
   *
   * The endpoint defaults to `detail: "metadata"`, so `content`, `source_query`,
   * `max_tokens` and `trigger` come back null unless you ask for them.
   */
  async listMentalModels(
    bankId: string,
    options?: {
      tags?: string[];
      tagsMatch?: "any" | "all" | "exact";
      /** Exclude large provenance chains with "metadata" or "content" when they are not needed. */
      detail?: "metadata" | "content" | "full";
      limit?: number;
      offset?: number;
      signal?: AbortSignal;
    },
  ): Promise<MentalModelListResponse> {
    const response = await sdk.listMentalModels({
      client: this.client,
      path: { bank_id: bankId },
      query: {
        tags: options?.tags,
        ...(options?.tagsMatch !== undefined ? { tags_match: options.tagsMatch } : {}),
        ...(options?.detail !== undefined ? { detail: options.detail } : {}),
        ...(options?.limit !== undefined ? { limit: options.limit } : {}),
        ...(options?.offset !== undefined ? { offset: options.offset } : {}),
      },
      signal: options?.signal,
    });

    return this.validateResponse(response, "listMentalModels");
  }

  /** Get a specific mental model. */
  async getMentalModel(
    bankId: string,
    mentalModelId: string,
    options?: {
      /** Exclude large provenance chains with "metadata" or "content" when they are not needed. */
      detail?: "metadata" | "content" | "full";
      signal?: AbortSignal;
    },
  ): Promise<MentalModelResponse> {
    const response = await sdk.getMentalModel({
      client: this.client,
      path: { bank_id: bankId, mental_model_id: mentalModelId },
      ...(options?.detail ? { query: { detail: options.detail } } : {}),
      signal: options?.signal,
    });

    return this.validateResponse(response, "getMentalModel");
  }

  /** Refresh a mental model against the bank's current knowledge. */
  async refreshMentalModel(
    bankId: string,
    mentalModelId: string,
    options?: { signal?: AbortSignal },
  ): Promise<AsyncOperationSubmitResponse> {
    const response = await sdk.refreshMentalModel({
      client: this.client,
      path: { bank_id: bankId, mental_model_id: mentalModelId },
      signal: options?.signal,
    });

    return this.validateResponse(response, "refreshMentalModel");
  }

  /**
   * Preview what a refresh would do to a mental model, without changing it.
   *
   * The production refresh pipeline with the two writes skipped, so what it
   * reports is what the next refresh will do.
   */
  async dryRunRefreshMentalModel(
    bankId: string,
    mentalModelId: string,
    options?: { signal?: AbortSignal },
  ): Promise<MentalModelDryRunRefreshResult> {
    const response = await sdk.dryRunRefreshMentalModel({
      client: this.client,
      path: { bank_id: bankId, mental_model_id: mentalModelId },
      signal: options?.signal,
    });

    return this.validateResponse(response, "dryRunRefreshMentalModel");
  }

  /** Clear a mental model's content, so the next refresh re-synthesises it in full. */
  async clearMentalModel(
    bankId: string,
    mentalModelId: string,
    options?: { signal?: AbortSignal },
  ): Promise<MentalModelResponse> {
    const response = await sdk.clearMentalModel({
      client: this.client,
      path: { bank_id: bankId, mental_model_id: mentalModelId },
      signal: options?.signal,
    });

    return this.validateResponse(response, "clearMentalModel");
  }

  /** Update a mental model's metadata. */
  async updateMentalModel(
    bankId: string,
    mentalModelId: string,
    options: {
      name?: string;
      sourceQuery?: string;
      tags?: string[];
      maxTokens?: number;
      /** Refresh settings to change, applied as a patch: the fields you send are updated and the rest keep their current values. */
      trigger?: MentalModelTriggerOptions;
      signal?: AbortSignal;
    },
  ): Promise<MentalModelResponse> {
    const response = await sdk.updateMentalModel({
      client: this.client,
      path: { bank_id: bankId, mental_model_id: mentalModelId },
      body: {
        name: options.name,
        source_query: options.sourceQuery,
        tags: options.tags,
        max_tokens: options.maxTokens,
        trigger: options.trigger ? toTriggerBody(options.trigger) : undefined,
      },
      signal: options.signal,
    });

    return this.validateResponse(response, "updateMentalModel");
  }

  /** Delete a mental model. */
  async deleteMentalModel(
    bankId: string,
    mentalModelId: string,
    options?: { signal?: AbortSignal },
  ): Promise<void> {
    const response = await sdk.deleteMentalModel({
      client: this.client,
      path: { bank_id: bankId, mental_model_id: mentalModelId },
      signal: options?.signal,
    });
    if (response.error) {
      throw new Error(`deleteMentalModel failed: ${JSON.stringify(response.error)}`);
    }
  }

  /** Get the change history of a mental model. */
  async getMentalModelHistory(
    bankId: string,
    mentalModelId: string,
    options?: { signal?: AbortSignal },
  ): Promise<unknown> {
    const response = await sdk.getMentalModelHistory({
      client: this.client,
      path: { bank_id: bankId, mental_model_id: mentalModelId },
      signal: options?.signal,
    });

    return this.validateResponse(response, "getMentalModelHistory");
  }

  /**
   * Get the knowledge base as a nested folder/page tree.
   *
   * Page bodies are not included — fetch one with {@link WenYuanClient.getKnowledgePage}.
   */
  async getKnowledgeBaseTree(
    bankId: string,
    options?: KnowledgeTagFilterOptions & { signal?: AbortSignal },
  ): Promise<KnowledgeTreeResponse> {
    const response = await sdk.getKnowledgeBaseTree({
      client: this.client,
      path: { bank_id: bankId },
      query: knowledgeTagQuery(options),
      signal: options?.signal,
    });

    return this.validateResponse(response, "getKnowledgeBaseTree");
  }

  /** Create a knowledge-base folder. */
  async createKnowledgeFolder(
    bankId: string,
    name: string,
    options?: { parentId?: string | null; signal?: AbortSignal },
  ): Promise<KnowledgeNode> {
    const response = await sdk.createKnowledgeFolder({
      client: this.client,
      path: { bank_id: bankId },
      body: { name, parent_id: options?.parentId },
      signal: options?.signal,
    });

    return this.validateResponse(response, "createKnowledgeFolder");
  }

  /**
   * Create a knowledge-base page. Its content is generated asynchronously — poll
   * the returned `operation_id` to know when the first build has finished.
   *
   * Omit `trigger` to use the page defaults; a supplied trigger is applied as a
   * patch over them, so the fields you leave out keep their defaults.
   */
  async createKnowledgePage(
    bankId: string,
    name: string,
    sourceQuery: string,
    options?: {
      parentId?: string | null;
      /** Scopes which memories the page is built from — a filter, not labels. A tagged page defaults to `all_strict`. */
      tags?: string[];
      maxTokens?: number;
      trigger?: MentalModelTriggerOptions;
      signal?: AbortSignal;
    },
  ): Promise<CreateKnowledgePageResponse> {
    const response = await sdk.createKnowledgePage({
      client: this.client,
      path: { bank_id: bankId },
      body: {
        name,
        source_query: sourceQuery,
        parent_id: options?.parentId,
        tags: options?.tags,
        max_tokens: options?.maxTokens,
        trigger: options?.trigger ? toTriggerBody(options.trigger) : undefined,
      },
      signal: options?.signal,
    });

    return this.validateResponse(response, "createKnowledgePage");
  }

  /** Get a knowledge page rendered as a markdown document (frontmatter + body). */
  async getKnowledgePage(
    bankId: string,
    pageId: string,
    options?: { signal?: AbortSignal },
  ): Promise<KnowledgePageResponse> {
    const response = await sdk.getKnowledgePage({
      client: this.client,
      path: { bank_id: bankId, page_id: pageId },
      signal: options?.signal,
    });

    return this.validateResponse(response, "getKnowledgePage");
  }

  /** Hybrid search (full-text + vector) over a bank's knowledge pages. */
  async searchKnowledgeBase(
    bankId: string,
    query: string,
    options?: KnowledgeTagFilterOptions & { limit?: number; signal?: AbortSignal },
  ): Promise<KnowledgePageSearchResponse> {
    const response = await sdk.searchKnowledgeBase({
      client: this.client,
      path: { bank_id: bankId },
      query: {
        q: query,
        ...(options?.limit !== undefined ? { limit: options.limit } : {}),
        ...knowledgeTagQuery(options),
      },
      signal: options?.signal,
    });

    return this.validateResponse(response, "searchKnowledgeBase");
  }

  /**
   * Rename or move a knowledge node, and/or update a page's options.
   *
   * Only the fields present in `options` are sent, so passing `parentId: null`
   * explicitly moves the node to the root.
   */
  async updateKnowledgeNode(
    bankId: string,
    nodeId: string,
    options: {
      name?: string;
      parentId?: string | null;
      /** Pages only — changing it rebuilds the page against the new question. */
      sourceQuery?: string;
      /** Pages only — replaces the page's tags (pass [] to clear). */
      tags?: string[];
      maxTokens?: number;
      /** Pages only — refresh settings to change, applied as a patch. */
      trigger?: MentalModelTriggerInput;
      signal?: AbortSignal;
    },
  ): Promise<KnowledgeNode> {
    const response = await sdk.updateKnowledgeNode({
      client: this.client,
      path: { bank_id: bankId, node_id: nodeId },
      body: {
        ...(options.name !== undefined ? { name: options.name } : {}),
        ...("parentId" in options ? { parent_id: options.parentId } : {}),
        ...(options.sourceQuery !== undefined ? { source_query: options.sourceQuery } : {}),
        ...(options.tags !== undefined ? { tags: options.tags } : {}),
        ...(options.maxTokens !== undefined ? { max_tokens: options.maxTokens } : {}),
        ...(options.trigger !== undefined ? { trigger: options.trigger } : {}),
      },
      signal: options.signal,
    });

    return this.validateResponse(response, "updateKnowledgeNode");
  }

  /** Delete a knowledge folder or page and its whole subtree. */
  async deleteKnowledgeNode(
    bankId: string,
    nodeId: string,
    options?: { signal?: AbortSignal },
  ): Promise<unknown> {
    const response = await sdk.deleteKnowledgeNode({
      client: this.client,
      path: { bank_id: bankId, node_id: nodeId },
      signal: options?.signal,
    });

    return this.validateResponse(response, "deleteKnowledgeNode");
  }

  /** Export the knowledge base as a portable markdown bundle. */
  async exportKnowledgeBase(
    bankId: string,
    options?: { signal?: AbortSignal },
  ): Promise<KnowledgePageBundleResponse> {
    const response = await sdk.exportKnowledgeBase({
      client: this.client,
      path: { bank_id: bankId },
      signal: options?.signal,
    });

    return this.validateResponse(response, "exportKnowledgeBase");
  }

  /** Get a document by id. Returns null if it does not exist. */
  async getDocument(
    bankId: string,
    documentId: string,
    options?: { signal?: AbortSignal },
  ): Promise<DocumentResponse | null> {
    const response = await sdk.getDocument({
      client: this.client,
      path: { bank_id: bankId, document_id: documentId },
      signal: options?.signal,
    });

    if (response.response?.status === 404) return null;

    return this.validateResponse(response, "getDocument");
  }

  /** List a bank's documents. */
  async listDocuments(
    bankId: string,
    options?: {
      limit?: number;
      offset?: number;
      /** Time axis to filter and order by; `updated_at` is the default ordering. */
      timeField?: "created_at" | "updated_at";
      /** ISO-8601, inclusive. */
      startDate?: string;
      /** ISO-8601, exclusive. */
      endDate?: string;
      signal?: AbortSignal;
    },
  ): Promise<ListDocumentsResponse> {
    const response = await sdk.listDocuments({
      client: this.client,
      path: { bank_id: bankId },
      query: {
        limit: options?.limit,
        offset: options?.offset,
        time_field: options?.timeField,
        start_date: options?.startDate,
        end_date: options?.endDate,
      },
      signal: options?.signal,
    });

    return this.validateResponse(response, "listDocuments");
  }

  /** Delete a document. */
  async deleteDocument(
    bankId: string,
    documentId: string,
    options?: { signal?: AbortSignal },
  ): Promise<void> {
    const response = await sdk.deleteDocument({
      client: this.client,
      path: { bank_id: bankId, document_id: documentId },
      signal: options?.signal,
    });
    if (response.error) {
      throw new Error(`deleteDocument failed: ${JSON.stringify(response.error)}`);
    }
  }

  /** Update a document's mutable fields. */
  async updateDocument(
    bankId: string,
    documentId: string,
    options: { tags?: string[]; signal?: AbortSignal },
  ): Promise<UpdateDocumentResponse> {
    const response = await sdk.updateDocument({
      client: this.client,
      path: { bank_id: bankId, document_id: documentId },
      body: { tags: options.tags },
      signal: options.signal,
    });

    return this.validateResponse(response, "updateDocument");
  }

  /**
   * Export a bank's documents as a transfer ZIP archive (blocking convenience).
   *
   * The export runs as a background operation server-side; this helper submits
   * it, polls the operation to completion, downloads the archive, and resolves
   * with its bytes. For the raw flow use `sdk.exportDocuments` /
   * `sdk.getOperationStatus` / `sdk.downloadFile`.
   *
   * @throws {WenYuanError} if the export fails, times out, or completes without
   * an archive.
   */
  async exportDocuments(
    bankId: string,
    options?: {
      documentIds?: string[];
      includeObservations?: boolean;
      includeKnowledgeBase?: boolean;
      /** Milliseconds between operation-status polls (default 2000). */
      pollIntervalMs?: number;
      /** Maximum milliseconds to wait for the export to finish (default 300000). */
      timeoutMs?: number;
      signal?: AbortSignal;
    },
  ): Promise<Uint8Array> {
    const submitResponse = await sdk.exportDocuments({
      client: this.client,
      path: { bank_id: bankId },
      query: {
        ...(options?.documentIds !== undefined ? { document_id: options.documentIds } : {}),
        ...(options?.includeObservations !== undefined
          ? { include_observations: options.includeObservations }
          : {}),
        ...(options?.includeKnowledgeBase !== undefined
          ? { include_knowledge_base: options.includeKnowledgeBase }
          : {}),
      },
      signal: options?.signal,
    });
    const submission = this.validateResponse(submitResponse, "exportDocuments");
    return this.downloadOperationArchive(bankId, submission.operation_id, options);
  }

  /**
   * Export a whole bank as a transfer ZIP archive (blocking convenience).
   *
   * Three flags decide what the archive carries. `includeData` covers the
   * memories and everything backing them; `includeBankConfig` the bank's own
   * config, mental models and their history, knowledge pages, directives and
   * webhooks; `includeHistory` the audit and LLM-request logs. Embeddings never
   * travel — the importing instance regenerates them.
   *
   * @throws {WenYuanError} if the export fails, times out, or completes without
   * an archive.
   */
  async exportBank(
    bankId: string,
    options?: {
      includeData?: boolean;
      includeBankConfig?: boolean;
      includeHistory?: boolean;
      /** Milliseconds between operation-status polls (default 2000). */
      pollIntervalMs?: number;
      /** Maximum milliseconds to wait for the export to finish (default 300000). */
      timeoutMs?: number;
      signal?: AbortSignal;
    },
  ): Promise<Uint8Array> {
    const submitResponse = await sdk.exportBankTransfer({
      client: this.client,
      path: { bank_id: bankId },
      query: {
        ...(options?.includeData !== undefined ? { include_data: options.includeData } : {}),
        ...(options?.includeBankConfig !== undefined
          ? { include_bank_config: options.includeBankConfig }
          : {}),
        ...(options?.includeHistory !== undefined
          ? { include_history: options.includeHistory }
          : {}),
      },
      signal: options?.signal,
    });
    const submission = this.validateResponse(submitResponse, "exportBankTransfer");
    return this.downloadOperationArchive(bankId, submission.operation_id, options);
  }

  /**
   * Restore a transfer archive into a fresh bank, and resolve with the
   * operation id.
   *
   * The restore runs in the background (facts are re-embedded and entities
   * re-resolved); poll `sdk.getOperationStatus` for its progress and counts.
   *
   * `targetBankId` must NOT already exist — this restores a whole bank rather
   * than merging into one. `bankId` is simply the bank the operation is
   * recorded against, because the target does not exist yet.
   */
  async importBank(
    bankId: string,
    archive: Blob | File,
    options?: {
      targetBankId?: string;
      includeData?: boolean;
      includeBankConfig?: boolean;
      includeHistory?: boolean;
      signal?: AbortSignal;
    },
  ): Promise<string> {
    const response = await sdk.importBankTransfer({
      client: this.client,
      path: { bank_id: bankId },
      query: {
        ...(options?.targetBankId !== undefined ? { target_bank_id: options.targetBankId } : {}),
        ...(options?.includeData !== undefined ? { include_data: options.includeData } : {}),
        ...(options?.includeBankConfig !== undefined
          ? { include_bank_config: options.includeBankConfig }
          : {}),
        ...(options?.includeHistory !== undefined
          ? { include_history: options.includeHistory }
          : {}),
      },
      body: { file: archive },
      signal: options?.signal,
    });
    const submission = this.validateResponse(response, "importBankTransfer");
    return submission.operation_id;
  }

  /**
   * Copy a bank into a new one, and resolve with the clone operation's id.
   *
   * The clone starts with the source's memories as they are at clone time and
   * evolves independently from then on. It runs server-side as the export and
   * import back to back, so no archive travels over the wire and no LLM is
   * called; poll `sdk.getOperationStatus` for progress.
   *
   * `targetBankId` must NOT already exist. Webhooks travel with
   * `includeBankConfig`, so a clone made with the defaults will call the
   * source's webhook endpoints.
   */
  async cloneBank(
    bankId: string,
    targetBankId: string,
    options?: {
      includeData?: boolean;
      includeBankConfig?: boolean;
      includeHistory?: boolean;
      signal?: AbortSignal;
    },
  ): Promise<string> {
    const response = await sdk.cloneBank({
      client: this.client,
      path: { bank_id: bankId },
      query: {
        target_bank_id: targetBankId,
        ...(options?.includeData !== undefined ? { include_data: options.includeData } : {}),
        ...(options?.includeBankConfig !== undefined
          ? { include_bank_config: options.includeBankConfig }
          : {}),
        ...(options?.includeHistory !== undefined
          ? { include_history: options.includeHistory }
          : {}),
      },
      signal: options?.signal,
    });
    const submission = this.validateResponse(response, "cloneBank");
    return submission.operation_id;
  }

  /**
   * Poll an export operation to completion and download the archive it
   * produced. Shared by {@link WenYuanClient.exportDocuments} and
   * {@link WenYuanClient.exportBank} — both submit an operation whose
   * `result_metadata` names the finished archive.
   */
  private async downloadOperationArchive(
    bankId: string,
    operationId: string,
    options?: { pollIntervalMs?: number; timeoutMs?: number; signal?: AbortSignal },
  ): Promise<Uint8Array> {
    const pollInterval = options?.pollIntervalMs ?? 2000;
    const timeout = options?.timeoutMs ?? 300000;
    const deadline = Date.now() + timeout;
    const timedOut = () =>
      new WenYuanError(
        `Export operation ${operationId} did not complete within ${timeout}ms`,
        0,
        undefined,
      );
    let resultMetadata: Record<string, unknown> | null | undefined;
    for (;;) {
      // The deadline bounds the in-flight status request and the sleep too, so
      // a slow poll or a long pollIntervalMs cannot overrun it.
      const remaining = deadline - Date.now();
      if (remaining <= 0) throw timedOut();
      let timer: ReturnType<typeof setTimeout> | undefined;
      const expired = new Promise<never>((_, reject) => {
        timer = setTimeout(() => reject(timedOut()), remaining);
      });
      let statusResponse;
      try {
        statusResponse = await Promise.race([
          sdk.getOperationStatus({
            client: this.client,
            path: { bank_id: bankId, operation_id: operationId },
            signal: options?.signal,
          }),
          expired,
        ]);
      } finally {
        clearTimeout(timer);
      }
      const status = this.validateResponse(
        statusResponse as ApiResult<OperationStatusResponse>,
        "getOperationStatus",
      );
      if (status.status === "completed") {
        resultMetadata = status.result_metadata;
        break;
      }
      if (status.status === "failed" || status.status === "cancelled") {
        throw new WenYuanError(
          `Export operation ${operationId} ${status.status}: ${status.error_message ?? ""}`,
          0,
          undefined,
        );
      }
      await new Promise((resolve) =>
        setTimeout(resolve, Math.min(pollInterval, Math.max(deadline - Date.now(), 0))),
      );
    }

    const downloadUrl = (resultMetadata as { download_url?: string } | null | undefined)
      ?.download_url;
    if (!downloadUrl) {
      throw new WenYuanError(
        `Export operation ${operationId} completed without a download_url`,
        0,
        undefined,
      );
    }
    if (/^https?:\/\//i.test(downloadUrl)) {
      // A signed object-store URL needs its own request: this client prefixes
      // its base URL and attaches the API key to every request it makes, and
      // neither belongs on somebody else's storage host.
      const response = await fetch(downloadUrl, { signal: options?.signal });
      if (!response.ok) {
        throw new WenYuanError(
          `downloadFile failed: HTTP ${response.status}`,
          response.status,
          undefined,
        );
      }
      return new Uint8Array(await response.arrayBuffer());
    }
    // An API-relative download_url carries the raw, slash-bearing storage key,
    // so it is fetched as given rather than through the templated download
    // route, which would percent-encode the slashes.
    const downloadResponse = await this.client.get({
      url: downloadUrl,
      parseAs: "arrayBuffer",
      signal: options?.signal,
    });
    const data = this.validateResponse(
      downloadResponse as ApiResult<ArrayBuffer>,
      "downloadFile",
    );
    return new Uint8Array(data);
  }
}