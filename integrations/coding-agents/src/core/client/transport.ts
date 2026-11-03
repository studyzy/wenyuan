/**
 * The seam between the MemoryClient class and the domain modules: the parts of the client that
 * do the actual talking, typed once so retain/knowledge/reflect/bank-config can be written as
 * plain functions over it.
 */

import type { ObservationScopes } from "./constants";

/** What the domain functions need from the client that calls them. Implemented by MemoryClient. */
export interface ClientInternals {
  readonly apiUrl: string;
  readonly bank: string;
  readonly project: string | undefined;
  readonly log: (msg: string) => void;
  readonly maxParallelRetains: number;
  readonly observationScopes: ObservationScopes;
  readonly pageSearchLimit: number;
  readonly recallOptions: Record<string, unknown>;
  /** Async operation ids collected by retain(), for drain(). */
  readonly opIds: string[];
  /** Tri-state capability probe: unknown until the first page request, then cached. */
  knowledgePagesSupported: boolean | undefined;
  /** Tri-state capability probe: unknown until the first append-mode retain, then cached. */
  idempotentRetain: boolean | undefined;
  bankUrl(suffix?: string): string;
  req(
    method: string,
    url: string,
    body?: unknown,
    tolerate?: number[],
    timeoutMs?: number
  ): Promise<Response>;
  fetchWithAuth(url: string, init: RequestInit): Promise<Response>;
  authHint(status: number): string;
}

/** Undici's cross-version handle on the dispatcher Node's global fetch uses. */
const GLOBAL_DISPATCHER = Symbol.for("undici.globalDispatcher.1");

/**
 * The global dispatcher with undici's own header/body timeouts switched off, for a request that
 * already carries an AbortSignal deadline — or undefined where there is nothing to wrap.
 *
 * Node's fetch applies a 300s headersTimeout below our signal, so a reflect configured above
 * 300s died with UND_ERR_HEADERS_TIMEOUT before the server could answer. Composing over the
 * CURRENT global dispatcher, instead of building a new undici Agent, needs no undici dependency
 * (the bundled one is not importable) and keeps any proxy dispatcher the host installed. Hosts
 * whose fetch is not undici's have no such dispatcher and ignore the option.
 */
export function signalOnlyDispatcher(): unknown {
  // Node loads undici — which installs the global dispatcher — lazily. Touching the Headers
  // global loads it; touching fetch does not (it is a wrapper until first called).
  void globalThis.Headers;
  const d = (globalThis as Record<symbol, unknown>)[GLOBAL_DISPATCHER] as
    | { compose?: (interceptor: unknown) => unknown }
    | undefined;
  if (typeof d?.compose !== "function") return undefined; // Node 18's undici predates compose()
  type Dispatch = (opts: Record<string, unknown>, handler: unknown) => boolean;
  return d.compose(
    (dispatch: Dispatch) =>
      (opts: Record<string, unknown>, handler: unknown) =>
        dispatch({ ...opts, headersTimeout: 0, bodyTimeout: 0 }, handler)
  );
}
