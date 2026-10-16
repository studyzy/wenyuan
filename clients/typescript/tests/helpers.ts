/**
 * A `fetch` stand-in that records every request and replays a canned answer.
 *
 * The mapping suites assert what a wrapper puts on the wire: the URL, the query
 * string and the JSON body. Recording the real request the generated client
 * would have sent tests that mapping end to end — nothing here needs a server.
 */

/** One request the stub observed. */
export interface RecordedRequest {
  url: string;
  /** Path and query, with the base URL stripped. */
  path: string;
  /** The path alone, without the query string. */
  pathname: string;
  method: string;
  headers: Record<string, string>;
  /** Query parameters, with repeated names preserved. */
  query: URLSearchParams;
  /** The decoded JSON body, when the request carried one. */
  body: unknown;
  /** The body as it went out on the wire. */
  text: string;
}

/** What the stub answers with. */
export interface StubReply {
  status?: number;
  /** A JSON payload to replay. */
  json?: unknown;
  /** Raw bytes to replay, for archive downloads. */
  bytes?: Uint8Array;
  headers?: Record<string, string>;
}

/** A reply for one recorded request, chosen by the caller. */
export type StubHandler = (
  request: RecordedRequest,
  index: number,
) => StubReply | Response | Promise<StubReply | Response>;

/** A recording `fetch` and the requests it has seen. */
export interface Stub {
  calls: RecordedRequest[];
  fetch: typeof fetch;
}

/** Build a recording `fetch` that answers each request from `handler`. */
export function stubFetch(handler: StubHandler): Stub {
  const calls: RecordedRequest[] = [];
  const impl = (async (input: string | URL | Request, init?: RequestInit) => {
    const request = input instanceof Request ? input : new Request(input, init);
    const url = new URL(request.url);
    const text = await request.text();
    const contentType = request.headers.get("content-type") ?? "";
    const recorded: RecordedRequest = {
      url: request.url,
      path: url.pathname + url.search,
      pathname: url.pathname,
      method: request.method,
      headers: Object.fromEntries(request.headers.entries()),
      query: url.searchParams,
      body:
        contentType.includes("json") && text !== ""
          ? JSON.parse(text)
          : undefined,
      text,
    };
    calls.push(recorded);

    const reply = await handler(recorded, calls.length - 1);
    if (reply instanceof Response) return reply;
    const headers = new Headers(reply.headers);
    let body: BodyInit | null = null;
    if (reply.bytes !== undefined) {
      body = reply.bytes as unknown as BodyInit;
    } else if (reply.json !== undefined) {
      headers.set("content-type", "application/json");
      body = JSON.stringify(reply.json);
    }
    return new Response(body, { status: reply.status ?? 200, headers });
  }) as typeof fetch;
  return { calls, fetch: impl };
}

/** The request recorded at `index`, failing loudly rather than returning undefined. */
export function call(stub: Stub, index = 0): RecordedRequest {
  const request = stub.calls[index];
  if (request === undefined) {
    throw new Error(`no request recorded at index ${index} (saw ${stub.calls.length})`);
  }
  return request;
}

/** Narrow a decoded JSON value to an object, failing loudly when it is not one. */
export function jsonObject(value: unknown, label: string): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error(`${label} is not a JSON object: ${JSON.stringify(value)}`);
  }
  // Checked immediately above, so the assertion cannot hide a wrong shape.
  return value as Record<string, unknown>;
}

/** The decoded JSON body of a recorded request, as an object. */
export function jsonBody(request: RecordedRequest): Record<string, unknown> {
  return jsonObject(request.body, "request body");
}