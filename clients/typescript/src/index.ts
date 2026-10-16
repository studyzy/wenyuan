/**
 * WenYuan TypeScript client.
 *
 * A thin façade over the generated SDK: `createClient()` builds a configured
 * client (base URL from the environment, bearer key from the environment) and
 * `retain` / `recall` / `reflect` are the convenience helpers for the three core
 * memory operations. Everything the generator produced is re-exported, so the
 * full 99-operation surface is reachable through this same entry point.
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

/** A memory to retain: bare text, or a full item (timestamp, tags, blocks, ...). */
export type RetainContent = string | MemoryItem;

/** Everything {@link retain} accepts besides the content itself. */
export type RetainOptions = Omit<RetainRequest, "items">;

/** Everything {@link recall} accepts besides the query. */
export type RecallOptions = Omit<RecallRequest, "query">;

/** Everything {@link reflect} accepts besides the query. */
export type ReflectOptions = Omit<ReflectRequest, "query">;

/** Options for {@link createClient}. */
export interface WenYuanClientOptions {
  /** API base URL; falls back to {@link ENV_BASE_URL}, then {@link DEFAULT_BASE_URL}. */
  baseUrl?: string;
  /** Bearer key; falls back to {@link ENV_API_KEY}. Omitted when neither is set. */
  apiKey?: string;
  /** Headers merged into every request, after the defaults. */
  headers?: HeadersInit;
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
 * Build a client pointed at WenYuan.
 *
 * The base URL comes from `options.baseUrl`, else `HINDSIGHT_API_URL`, else
 * `http://localhost:18888`; the bearer key (used only when present, so an open
 * deployment needs no configuration) comes from `options.apiKey`, else
 * `HINDSIGHT_API_KEY`.
 */
export function createClient(options: WenYuanClientOptions = {}): Client {
  const apiKey = options.apiKey ?? env(ENV_API_KEY);
  const config: Config = {
    ...options.requestInit,
    baseUrl: resolveBaseUrl(options.baseUrl),
    fetch: options.fetch,
    responseStyle: options.responseStyle,
    headers: mergeHeaders(
      { "User-Agent": DEFAULT_USER_AGENT },
      options.headers,
      apiKey === undefined ? undefined : { Authorization: `Bearer ${apiKey}` },
    ),
  };
  return createGeneratedClient(createConfig(config));
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
  return unwrap(
    "retain",
    sdk.retainMemories({
      client,
      path: { bank_id: bankId },
      body: { ...options, items: toItems(content) },
    }),
  );
}

/** Search a bank's memories for a query. */
export function recall(
  client: Client,
  bankId: string,
  query: string,
  options: RecallOptions = {},
): Promise<RecallResponse> {
  return unwrap(
    "recall",
    sdk.recallMemories({
      client,
      path: { bank_id: bankId },
      body: { ...options, query },
    }),
  );
}

/** Synthesise an answer to a query from a bank's memories. */
export function reflect(
  client: Client,
  bankId: string,
  query: string,
  options: ReflectOptions = {},
): Promise<ReflectResponse> {
  return unwrap(
    "reflect",
    sdk.reflect({
      client,
      path: { bank_id: bankId },
      body: { ...options, query },
    }),
  );
}

