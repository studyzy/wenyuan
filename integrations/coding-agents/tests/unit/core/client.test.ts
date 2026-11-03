import { afterEach, describe, expect, it, vi } from "vitest";
import {
  DEFAULT_MAX_PARALLEL_RETAINS,
  DEFAULT_OBSERVATION_SCOPES,
  DEFAULT_PAGE_SEARCH_LIMIT,
  DEFAULT_RECALL_OPTIONS,
  MemoryClient,
  ReflectError,
  retryAfterMs,
  USER_AGENT,
} from "../../../src/core/client";
import { DEFAULT_REQUEST_TIMEOUT_MS } from "../../../src/core/client/constants";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

function jsonResponse(status: number, body: unknown, headers?: Record<string, string>): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ...(headers ?? {}) },
  });
}

describe("MemoryClient defaults", () => {
  it("caps concurrent retain-related requests at 10 unless configured", () => {
    expect(new MemoryClient({ apiUrl: "http://x", bank: "b" }).maxParallelRetains).toBe(
      DEFAULT_MAX_PARALLEL_RETAINS
    );
    expect(
      new MemoryClient({ apiUrl: "http://x", bank: "b", maxParallelRetains: 3 }).maxParallelRetains
    ).toBe(3);
  });

  it("identifies itself with the integration's User-Agent", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(200, {}));
    vi.stubGlobal("fetch", fetchMock);
    await new MemoryClient({ apiUrl: "http://x", bank: "b" }).req("GET", "http://x/thing");
    expect(new Headers(fetchMock.mock.calls[0]![1]!.headers).get("User-Agent")).toBe(USER_AGENT);
    expect(USER_AGENT).toMatch(/^wenyuan-coding-agents\/\d+\.\d+\.\d+/);
  });

  it("asks the server not to compress responses — some hosts return gzip bytes undecoded", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(200, {}));
    vi.stubGlobal("fetch", fetchMock);
    await new MemoryClient({ apiUrl: "http://x", bank: "b" }).req("GET", "http://x/thing");
    expect(new Headers(fetchMock.mock.calls[0]![1]!.headers).get("Accept-Encoding")).toBe(
      "identity"
    );
  });

  it("bounds every ordinary request with a hard 15s signal deadline", async () => {
    const fetchMock = vi.fn(async (_url: string | URL, init?: RequestInit) => {
      expect(init?.signal).toBeInstanceOf(AbortSignal);
      return jsonResponse(200, {});
    });
    vi.stubGlobal("fetch", fetchMock);
    await new MemoryClient({ apiUrl: "http://x", bank: "b" }).req("GET", "http://x/thing");
    expect(DEFAULT_REQUEST_TIMEOUT_MS).toBe(15_000);
  });

  it("scopes document listings under the banked /v1/default/banks route", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(200, { items: [], total: 0 }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new MemoryClient({ apiUrl: "http://x/", bank: "shared-bank" });
    await client.hasDocuments();
    expect(String(fetchMock.mock.calls[0]![0])).toBe(
      "http://x/v1/default/banks/shared-bank/documents?limit=1"
    );
  });
});

describe("MemoryClient document-list safety", () => {
  it("paginates strategy-tag listings with strict matching on every page", async () => {
    const client = new MemoryClient({ apiUrl: "http://x", bank: "shared-bank" });
    const firstPage = Array.from({ length: 500 }, (_, i) => ({ id: `git:${i}` }));
    const fetchMock = vi.fn(async (url: string | URL | Request) => {
      const offset = String(url).includes("offset=500") ? 500 : 0;
      return jsonResponse(200, { items: offset === 0 ? firstPage : [{ id: "git:500" }], total: 501 });
    });
    vi.stubGlobal("fetch", fetchMock);

    const ids = await client.listDocumentIds("source:git", "all_strict");

    expect(ids.size).toBe(501);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(String(fetchMock.mock.calls[0]![0])).toBe(
      "http://x/v1/default/banks/shared-bank/documents?tags=source%3Agit&tags_match=all_strict&limit=500&offset=0"
    );
    expect(String(fetchMock.mock.calls[1]![0])).toBe(
      "http://x/v1/default/banks/shared-bank/documents?tags=source%3Agit&tags_match=all_strict&limit=500&offset=500"
    );
  });

  it("preserves the inclusive all mode for callers that do not opt into strict matching", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(200, { items: [], total: 0 }));
    vi.stubGlobal("fetch", fetchMock);
    await new MemoryClient({ apiUrl: "http://x", bank: "shared-bank" }).listDocumentIds(
      "custom:scope"
    );
    expect(String(fetchMock.mock.calls[0]![0])).toContain(
      "tags=custom%3Ascope&tags_match=all&limit=500&offset=0"
    );
  });
});

describe("MemoryClient.documentTags", () => {
  it("reads one document's tags from the id-filtered listing, not the full document", async () => {
    const client = new MemoryClient({ apiUrl: "http://x", bank: "shared-bank" });
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) =>
      jsonResponse(200, {
        items: [
          { id: "gitlog:repo-fork", tags: ["gitlog-head:aaa"] },
          { id: "gitlog:repo", tags: ["source:git-log", "gitlog-head:bbb"] },
        ],
        total: 2,
      })
    );
    vi.stubGlobal("fetch", fetchMock);

    expect(await client.documentTags("gitlog:repo")).toEqual([
      "source:git-log",
      "gitlog-head:bbb",
    ]);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(String(fetchMock.mock.calls[0]![0])).toBe(
      "http://x/v1/default/banks/shared-bank/documents?q=gitlog%3Arepo&limit=100&offset=0"
    );
  });

  it("pages past ids that only contain the requested one, and answers undefined without it", async () => {
    const client = new MemoryClient({ apiUrl: "http://x", bank: "shared-bank" });
    const lookalikes = Array.from({ length: 100 }, (_, i) => ({
      id: `gitlog:repo-${i}`,
      tags: [],
    }));
    const fetchMock = vi.fn(async (url: string | URL | Request) =>
      jsonResponse(200, {
        items: String(url).includes("offset=100")
          ? [{ id: "gitlog:repo-100", tags: [] }]
          : lookalikes,
        total: 101,
      })
    );
    vi.stubGlobal("fetch", fetchMock);

    expect(await client.documentTags("gitlog:repo")).toBeUndefined();
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(String(fetchMock.mock.calls[1]![0])).toContain("q=gitlog%3Arepo&limit=100&offset=100");
  });
});

describe("MemoryClient.hasDocuments", () => {
  it.each([
    [200, { items: [{ id: "a" }], total: 7 }, true],
    [200, { items: [], total: 0 }, false],
    [404, { detail: "Bank not found" }, false],
  ] as const)("status %s %j -> %s", async (status, body, expected) => {
    const client = new MemoryClient({ apiUrl: "http://x", bank: "b" });
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(status, body));
    vi.stubGlobal("fetch", fetchMock);
    expect(await client.hasDocuments()).toBe(expected);
  });

  it("throws on a server error, so a caller cannot read it as an empty bank", async () => {
    vi.stubGlobal("fetch", vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(500, { detail: "boom" })));
    await expect(new MemoryClient({ apiUrl: "http://x", bank: "b" }).hasDocuments()).rejects.toThrow();
  });
});

describe("MemoryClient.drain", () => {
  it("polls at most maxParallelRetains ops concurrently", async () => {
    const cap = 2;
    let inFlight = 0;
    let maxInFlight = 0;
    const fetchMock = vi.fn(async () => {
      inFlight++;
      maxInFlight = Math.max(maxInFlight, inFlight);
      await new Promise((r) => setTimeout(r, 5));
      inFlight--;
      return jsonResponse(200, { status: "completed" });
    });
    vi.stubGlobal("fetch", fetchMock);

    await new MemoryClient({ apiUrl: "http://x", bank: "b", maxParallelRetains: cap }).drain(
      ["1", "2", "3", "4", "5"],
      "test",
      10_000
    );

    expect(maxInFlight).toBeLessThanOrEqual(cap);
    expect(maxInFlight).toBe(cap); // 5 ids against a 2-wide pool must actually hit the cap
    expect(fetchMock).toHaveBeenCalledTimes(5);
  });

  it("backs off by Retry-After when it exceeds the 10s floor", async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(429, {}, { "Retry-After": "30" }));
    vi.stubGlobal("fetch", fetchMock);

    const p = new MemoryClient({ apiUrl: "http://x", bank: "b" }).drain(["1"], "test", 60_000);
    await vi.advanceTimersByTimeAsync(0); // first cycle completes -> sleep 30s
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(10_000); // floor elapsed, but the header says 30s
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(20_000); // 30s elapsed -> next cycle
    expect(fetchMock).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(60_000); // run out the cap
    await p;
  });

  it("caps the backoff however long Retry-After asks for", async () => {
    // The header is a hint, not a budget owed to the server: an hour-long value would otherwise
    // park the drain — and the background seed behind it — for that hour.
    vi.useFakeTimers();
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(429, {}, { "Retry-After": "3600" }));
    vi.stubGlobal("fetch", fetchMock);

    const p = new MemoryClient({ apiUrl: "http://x", bank: "b" }).drain(["1"], "test", 300_000);
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(60_000); // capped at 60s, not 3600s
    expect(fetchMock).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(300_000);
    await p;
  });

  it("uses the 10s floor when Retry-After is shorter than it", async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(429, {}, { "Retry-After": "2" }));
    vi.stubGlobal("fetch", fetchMock);

    const p = new MemoryClient({ apiUrl: "http://x", bank: "b" }).drain(["1"], "test", 60_000);
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(2_000); // Retry-After=2s would have fired by now
    expect(fetchMock).toHaveBeenCalledTimes(1); // the 10s floor keeps us waiting
    await vi.advanceTimersByTimeAsync(8_000);
    expect(fetchMock).toHaveBeenCalledTimes(2); // floor elapsed -> next cycle
    await vi.advanceTimersByTimeAsync(60_000);
    await p;
  });

  it("backs off 10s on a 429 that carries no Retry-After", async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(429, {}));
    vi.stubGlobal("fetch", fetchMock);

    const p = new MemoryClient({ apiUrl: "http://x", bank: "b" }).drain(["1"], "test", 60_000);
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(5_000); // the plain 5s cycle would fire here
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(5_000);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(60_000);
    await p;
  });

  it("keeps the 5s cycle when ops stay pending without a 429", async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(200, { status: "running" }));
    vi.stubGlobal("fetch", fetchMock);

    const p = new MemoryClient({ apiUrl: "http://x", bank: "b" }).drain(["1"], "test", 60_000);
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(5_000);
    expect(fetchMock).toHaveBeenCalledTimes(2); // 5s cycle preserved
    await vi.advanceTimersByTimeAsync(60_000);
    await p;
  });

  it("marks non-completed terminal ops as failed and drops them from polling", async () => {
    const fetchMock = vi.fn(async (url: string | URL | Request) => {
      const id = String(url).split("/").pop();
      return id === "1"
        ? jsonResponse(200, { status: "failed" })
        : jsonResponse(200, { status: "completed" });
    });
    vi.stubGlobal("fetch", fetchMock);
    const log = vi.fn();
    const c = new MemoryClient({ apiUrl: "http://x", bank: "b", log });

    await c.drain(["1", "2"], "test", 10_000);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(log).toHaveBeenCalledWith("[wait] test drained — 2 done, 1 failed");
  });

  it("a 403/404 poll fails the op at once instead of polling it until the timeout", async () => {
    for (const status of [403, 404]) {
      const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(status, { detail: "nope" }));
      vi.stubGlobal("fetch", fetchMock);
      const log = vi.fn();
      const c = new MemoryClient({ apiUrl: "http://x", bank: "b", log });

      await c.drain(["1"], "test", 60_000);
      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(log).toHaveBeenCalledWith(`  ... test op 1 -> HTTP ${status}, counted as failed`);
      expect(log).toHaveBeenCalledWith("[wait] test drained — 1 done, 1 failed");
    }
  });

  it("a 5xx poll is retried a bounded number of times, then fails the op", async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(503, {}));
    vi.stubGlobal("fetch", fetchMock);
    const log = vi.fn();
    const c = new MemoryClient({ apiUrl: "http://x", bank: "b", log });

    const p = c.drain(["1"], "test", 60 * 60 * 1000);
    await vi.advanceTimersByTimeAsync(5 * 60 * 1000);
    await p;
    expect(fetchMock).toHaveBeenCalledTimes(12);
    expect(log).toHaveBeenCalledWith("[wait] test drained — 1 done, 1 failed");
  });
});

describe("retryAfterMs", () => {
  it("parses delta-seconds", () => {
    expect(retryAfterMs("30")).toBe(30_000);
    expect(retryAfterMs(" 2 ")).toBe(2_000);
  });

  it("parses an HTTP-date into a bounded delay", () => {
    const future = new Date(Date.now() + 60_000).toUTCString();
    const ms = retryAfterMs(future);
    expect(ms).toBeGreaterThan(50_000);
    expect(ms).toBeLessThanOrEqual(60_000);
  });

  it("returns 0 for absent or unparseable values", () => {
    expect(retryAfterMs(null)).toBe(0);
    expect(retryAfterMs(undefined)).toBe(0);
    expect(retryAfterMs("")).toBe(0);
    expect(retryAfterMs("soon")).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// retain: observation scoping
// ---------------------------------------------------------------------------
describe("MemoryClient.retain — observation scoping", () => {
  async function retainItem(
    client: MemoryClient,
    tags: string[] = ["source:chat", "harness:claude-code"]
  ): Promise<Record<string, unknown>> {
    let sent: string | undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, init: RequestInit) => {
        sent = String(init.body);
        return jsonResponse(200, { operation_id: "op-1" });
      })
    );
    await client.retain("c", "ctx", "doc-1", tags, "conversation");
    const body = JSON.parse(String(sent)) as { items: Record<string, unknown>[] };
    return body.items[0]!;
  }

  it("defaults every retain to the single global scope, so two agents on one repo build ONE set of observations", async () => {
    expect(DEFAULT_OBSERVATION_SCOPES).toBe("shared");
    const item = await retainItem(new MemoryClient({ apiUrl: "http://x", bank: "b" }));
    expect(item.observation_scopes).toBe("shared");
    // The harness tag still travels: it is what the documents list filters and draws its logo from.
    expect(item.tags).toEqual(["source:chat", "harness:claude-code"]);
  });

  it("sends a configured scoping instead, including the server's own default", async () => {
    const combined = await retainItem(
      new MemoryClient({ apiUrl: "http://x", bank: "b", observationScopes: "combined" })
    );
    expect(combined.observation_scopes).toBe("combined");
    const explicit = await retainItem(
      new MemoryClient({ apiUrl: "http://x", bank: "b", observationScopes: [["project:demo"]] })
    );
    expect(explicit.observation_scopes).toEqual([["project:demo"]]);
  });

  it("sends observation_scopes on EVERY retain and collects the operation id for drain()", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(200, { operation_id: "op-9" }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new MemoryClient({ apiUrl: "http://x", bank: "b" });
    await client.retain("body", "ctx", "chat:1", [], "conversation", {
      timestamp: "2026-01-01T00:00:00Z",
      metadata: { project: "demo" },
      updateMode: "append",
      operationId: "req-1",
    });
    const [url, init] = fetchMock.mock.calls[0]!;
    expect(String(url)).toBe("http://x/v1/default/banks/b/memories");
    const body = JSON.parse(String(init!.body)) as Record<string, unknown> & {
      items: Record<string, unknown>[];
    };
    expect(body.async).toBe(true);
    expect(body.operation_id).toBe("req-1");
    expect(body.items[0]).toMatchObject({
      content: "body",
      context: "ctx",
      document_id: "chat:1",
      strategy: "conversation",
      update_mode: "append",
      timestamp: "2026-01-01T00:00:00Z",
      metadata: { project: "demo" },
      observation_scopes: "shared",
    });
    expect(client.opIds).toEqual(["op-9"]);
  });
});

// per_source is the one scoping a static config cannot express: the server treats an explicit
// scope list as UNCONDITIONAL, so a configured scope list would write every document into all
// three. Deriving the scopes per document from its own source: tags is what separates "what the
// commits say" from "what was discussed" while keeping the merged global set.
describe("MemoryClient.retain — per_source scoping", () => {
  async function retainItem(client: MemoryClient, tags: string[]): Promise<Record<string, unknown>> {
    let sent: string | undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, init: RequestInit) => {
        sent = String(init.body);
        return jsonResponse(200, { operation_id: "op-1" });
      })
    );
    await client.retain("c", "ctx", "doc-1", tags, "conversation");
    const body = JSON.parse(String(sent)) as { items: Record<string, unknown>[] };
    return body.items[0]!;
  }

  const perSource = () =>
    new MemoryClient({ apiUrl: "http://x", bank: "b", observationScopes: "per_source" });

  it("keeps the global scope and adds the document's own source scope", async () => {
    const item = await retainItem(perSource(), ["source:chat", "harness:claude-code"]);
    expect(item.observation_scopes).toEqual([[], ["source:chat"]]);
  });

  it("scopes a git document apart from a chat one", async () => {
    const item = await retainItem(perSource(), ["source:git", "harness:claude-code"]);
    expect(item.observation_scopes).toEqual([[], ["source:git"]]);
  });

  it("falls back to the global scope alone when a document carries no source tag", async () => {
    const item = await retainItem(perSource(), ["knowledge:convention"]);
    expect(item.observation_scopes).toEqual([[]]);
  });

  // A document may carry two source tags (the commit-message seed is both source:git and
  // source:git-log): two questions, two answers, each deduplicated within itself.
  it("gives a document carrying two source tags a scope for each", async () => {
    const item = await retainItem(perSource(), ["source:git", "source:git-log", "gitlog-head:abc"]);
    expect(item.observation_scopes).toEqual([[], ["source:git"], ["source:git-log"]]);
  });

  it("orders the scopes independently of the order the tags arrive in", async () => {
    const item = await retainItem(perSource(), ["source:git-log", "source:git"]);
    expect(item.observation_scopes).toEqual([[], ["source:git"], ["source:git-log"]]);
  });

  it("never lets a volatile provenance tag become a scope", async () => {
    const item = await retainItem(perSource(), ["source:chat", "session:20260829_101500_abc"]);
    expect(item.observation_scopes).toEqual([[], ["source:chat"]]);
  });
});

// ---------------------------------------------------------------------------
// rate-limit patience
// ---------------------------------------------------------------------------
describe("MemoryClient rate-limit patience", () => {
  const retainOnce = (client: MemoryClient) =>
    client.retain("chat body", "developer chat", "chat:1", [], "conversation");

  it("fails fast on a 429 by default — a hook answers to its host's deadline", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(429, {}, { "Retry-After": "0" }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new MemoryClient({ apiUrl: "http://x", bank: "b" });
    await expect(retainOnce(client)).rejects.toMatchObject({ code: "rate_limited" });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("waits out 429s and lands the write when given patience", async () => {
    // A limiter can send "Retry-After: 0"; background work that gave up on it dropped items.
    vi.useFakeTimers();
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(429, {}, { "Retry-After": "0" }))
      .mockResolvedValueOnce(jsonResponse(429, {}, { "Retry-After": "0" }))
      .mockResolvedValueOnce(jsonResponse(200, { operation_id: "op-1" }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new MemoryClient({ apiUrl: "http://x", bank: "b", rateLimitPatienceMs: 60_000 });
    const p = retainOnce(client);
    await vi.advanceTimersByTimeAsync(10_000);
    await p;
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(client.opIds).toEqual(["op-1"]);
    // the same payload every time — a retry must not change what is written
    const bodies = fetchMock.mock.calls.map((c) => (c[1] as RequestInit).body);
    expect(new Set(bodies).size).toBe(1);
  });

  it("gives up with RateLimitedError once the patience is spent", async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(429, {}, { "Retry-After": "0" }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new MemoryClient({ apiUrl: "http://x", bank: "b", rateLimitPatienceMs: 5_000 });
    const p = retainOnce(client);
    const settled = expect(p).rejects.toMatchObject({ code: "rate_limited" });
    await vi.advanceTimersByTimeAsync(60_000);
    await settled;
    expect(fetchMock.mock.calls.length).toBeGreaterThan(1);
    expect(fetchMock.mock.calls.length).toBeLessThan(6);
  });
});

// ---------------------------------------------------------------------------
// credential refresh: the ONE signed-send path recovers from a rotated key
// ---------------------------------------------------------------------------
describe("MemoryClient credential refresh", () => {
  /** Stub fetch that only accepts one bearer token, and records what it was asked. */
  function server(accepted: () => string | undefined) {
    const calls: { auth: string | null; body: string | null }[] = [];
    const fetchMock = vi.fn(async (_url: string, init: RequestInit) => {
      const auth = new Headers(init.headers).get("Authorization");
      calls.push({ auth, body: (init.body as string) ?? null });
      const want = accepted();
      if (auth !== (want ? `Bearer ${want}` : null))
        return jsonResponse(401, { detail: "Authentication failed: Invalid API key" });
      return jsonResponse(200, { ok: true });
    });
    vi.stubGlobal("fetch", fetchMock);
    return { calls, fetchMock };
  }

  it("recovers from a rotated credential without restarting the host", async () => {
    const { calls } = server(() => "new-key");
    let onDisk = "old-key";
    const client = new MemoryClient({
      apiUrl: "http://x",
      bank: "b",
      apiToken: onDisk,
      tokenProvider: () => onDisk,
    });

    onDisk = "new-key"; // the operator rotates the key while the host keeps running
    await client.req("GET", "http://x/thing");

    expect(calls.map((c) => c.auth)).toEqual(["Bearer old-key", "Bearer new-key"]);
    expect(client.apiToken).toBe("new-key");
  });

  it("does not retry when the credential is unchanged — a wrong key stays one 401, not two", async () => {
    const { calls } = server(() => "right-key");
    const client = new MemoryClient({
      apiUrl: "http://x",
      bank: "b",
      apiToken: "wrong-key",
      tokenProvider: () => "wrong-key",
    });

    await expect(client.req("GET", "http://x/thing")).rejects.toThrow(/401/);
    expect(calls).toHaveLength(1);
  });

  it("keeps the last credential that worked when the provider throws", async () => {
    const { calls } = server(() => "any");
    const client = new MemoryClient({
      apiUrl: "http://x",
      bank: "b",
      apiToken: "good-key",
      // A half-written config file must not leave the client with no credential at all.
      tokenProvider: () => {
        throw new Error("unparseable config");
      },
    });

    await expect(client.req("GET", "http://x/thing")).rejects.toThrow(/401/);
    expect(calls).toHaveLength(1);
    expect(client.apiToken).toBe("good-key");
  });

  it("leaves a provider-less client exactly as it was", async () => {
    const { calls } = server(() => "any");
    const client = new MemoryClient({ apiUrl: "http://x", bank: "b", apiToken: "stale" });

    await expect(client.req("GET", "http://x/thing")).rejects.toThrow(/401/);
    expect(calls).toHaveLength(1);
  });

  it("replays the body of a retried POST", async () => {
    const { calls } = server(() => "new-key");
    let onDisk = "old-key";
    const client = new MemoryClient({
      apiUrl: "http://x",
      bank: "b",
      apiToken: onDisk,
      tokenProvider: () => onDisk,
    });

    onDisk = "new-key";
    await client.req("POST", "http://x/thing", { hello: "world" });

    expect(calls.map((c) => c.body)).toEqual([
      JSON.stringify({ hello: "world" }),
      JSON.stringify({ hello: "world" }),
    ]);
  });

  // reflect() and the drain poll fetch outside req(), so a recovery wired only into req() would
  // have left them failing forever — which is how the original bug read: hooks worked, tools did not.
  it("recovers on the reflect path too, not just the generic request path", async () => {
    const { calls } = server(() => "new-key");
    let onDisk = "old-key";
    const client = new MemoryClient({
      apiUrl: "http://x",
      bank: "b",
      apiToken: onDisk,
      tokenProvider: () => onDisk,
    });

    onDisk = "new-key";
    await client.reflect("why?", { timeoutMs: 5_000 });
    expect(calls.map((c) => c.auth)).toEqual(["Bearer old-key", "Bearer new-key"]);
  });

  it("says whether a credential was even sent, which the server's 401 cannot", async () => {
    server(() => "needed");
    const none = new MemoryClient({ apiUrl: "http://x", bank: "b" });
    await expect(none.req("GET", "http://x/thing")).rejects.toThrow(/no apiToken is configured/);

    const wrong = new MemoryClient({ apiUrl: "http://x", bank: "b", apiToken: "nope" });
    await expect(wrong.req("GET", "http://x/thing")).rejects.toThrow(/was rejected/);
  });
});

// ---------------------------------------------------------------------------
// reflect / recall
// ---------------------------------------------------------------------------
describe("MemoryClient.reflect failures", () => {
  it("keeps the server's error body, not just the status", async () => {
    vi.stubGlobal("fetch", vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(500, { detail: "tool_call ids must be unique" })));
    const client = new MemoryClient({ apiUrl: "http://x", bank: "b" });

    await expect(client.reflect("why?", { timeoutMs: 5_000 })).rejects.toThrow(
      /^reflect 500 .*tool_call ids must be unique/
    );
  });

  it("names its own deadline instead of a bare 'operation was aborted'", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (_url: string | URL | Request, init?: RequestInit) =>
          new Promise<Response>((_resolve, reject) => {
            init?.signal?.addEventListener("abort", () => reject(init.signal?.reason));
          })
      )
    );
    const client = new MemoryClient({ apiUrl: "http://x", bank: "b" });

    await expect(client.reflect("why?", { timeoutMs: 10 })).rejects.toThrow(
      "reflect timed out after 10ms"
    );
  });

  it("types the failure so the hook can tell a fallback-worthy one apart", async () => {
    const client = new MemoryClient({ apiUrl: "http://x", bank: "b" });
    const failWith = async (status: number) => {
      vi.stubGlobal("fetch", vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(status, { detail: "x" })));
      return client.reflect("why?", { timeoutMs: 5_000 }).catch((e: unknown) => e);
    };

    const e503 = (await failWith(503)) as ReflectError;
    expect(e503).toBeInstanceOf(ReflectError);
    expect(e503.status).toBe(503);
    expect(e503.fallbackEligible).toBe(true);
    // A 4xx fails the same way on every endpoint (auth, missing bank): no fallback.
    expect((await failWith(401) as ReflectError).fallbackEligible).toBe(false);
    expect((await failWith(401) as ReflectError).permanent).toBe(true);
    // 408/429 are "not now", so they stay retryable.
    expect((await failWith(408) as ReflectError).permanent).toBe(false);

    vi.stubGlobal(
      "fetch",
      vi.fn(
        (_url: string | URL | Request, init?: RequestInit) =>
          new Promise<Response>((_resolve, reject) => {
            init?.signal?.addEventListener("abort", () => reject(init.signal?.reason));
          })
      )
    );
    const timeout = (await client.reflect("why?", { timeoutMs: 10 }).catch((e) => e)) as ReflectError;
    expect(timeout.timedOut).toBe(true);
    expect(timeout.fallbackEligible).toBe(true);
  });
});

describe("MemoryClient.recallObservations", () => {
  it("recalls only observations, low budget, no entities, and returns their texts in order", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) =>
      jsonResponse(200, { results: [{ text: " first " }, { text: "" }, { text: "second" }] })
    );
    vi.stubGlobal("fetch", fetchMock);
    const client = new MemoryClient({ apiUrl: "http://x", bank: "b" });

    const out = await client.recallObservations("goal", { timeoutMs: 5_000 });

    expect(out).toEqual(["first", "second"]);
    expect(DEFAULT_RECALL_OPTIONS).toEqual({
      types: ["observation"],
      budget: "low",
      max_tokens: 2000,
      include: { entities: null },
    });
    const [url, init] = fetchMock.mock.calls[0]!;
    expect(String(url)).toBe("http://x/v1/default/banks/b/memories/recall");
    expect(JSON.parse(String(init?.body))).toEqual({
      query: "goal",
      types: ["observation"],
      budget: "low",
      max_tokens: 2000,
      include: { entities: null },
    });
  });

  it("merges recallOptions key-by-key over the defaults, leaving untouched keys alone", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(200, { results: [{ text: "only" }] }));
    vi.stubGlobal("fetch", fetchMock);

    await new MemoryClient({
      apiUrl: "http://x",
      bank: "b",
      recallOptions: { types: ["world", "experience"], max_tokens: 250, tags: ["t"] },
    }).recallObservations("goal", { timeoutMs: 5_000 });

    expect(JSON.parse(String(fetchMock.mock.calls[0]![1]?.body))).toEqual({
      query: "goal",
      types: ["world", "experience"],
      max_tokens: 250,
      // An option the client knows nothing about rides along untouched — the point of the object.
      tags: ["t"],
      // Not overridden, so the defaults stand.
      budget: "low",
      include: { entities: null },
    });
  });

  it("asks for every fact type when recallOptions sets types to null", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(200, { results: [{ text: "only" }] }));
    vi.stubGlobal("fetch", fetchMock);

    await new MemoryClient({
      apiUrl: "http://x",
      bank: "b",
      recallOptions: { types: null },
    }).recallObservations("goal", { timeoutMs: 5_000 });

    expect(JSON.parse(String(fetchMock.mock.calls[0]![1]?.body)).types).toBeNull();
  });

  it("never lets recallOptions replace the query — the goal is not configurable", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(200, { results: [{ text: "only" }] }));
    vi.stubGlobal("fetch", fetchMock);

    await new MemoryClient({
      apiUrl: "http://x",
      bank: "b",
      recallOptions: { query: "a fixed string" },
    }).recallObservations("the real goal", { timeoutMs: 5_000 });

    expect(JSON.parse(String(fetchMock.mock.calls[0]![1]?.body)).query).toBe("the real goal");
  });

  it("answers empty on a 404 (bank not created yet)", async () => {
    vi.stubGlobal("fetch", vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(404, { detail: "Bank not found" })));
    const out = await new MemoryClient({ apiUrl: "http://x", bank: "b" }).recallObservations(
      "goal",
      { timeoutMs: 5_000 }
    );
    expect(out).toEqual([]);
  });
});

describe("page search defaults", () => {
  it("shares one search limit between the hook and the tool", () => {
    expect(DEFAULT_PAGE_SEARCH_LIMIT).toBe(10);
    expect(
      new MemoryClient({ apiUrl: "http://x", bank: "b", pageSearchLimit: 7 }).pageSearchLimit
    ).toBe(7);
  });

  it("copies recallOptions per client, so one caller's mutation is not everyone's", () => {
    const a = new MemoryClient({ apiUrl: "http://x", bank: "a" });
    const b = new MemoryClient({ apiUrl: "http://x", bank: "b" });
    expect(a.recallOptions).toEqual(b.recallOptions);
    expect(a.recallOptions).not.toBe(b.recallOptions);
    expect(a.recallOptions).not.toBe(DEFAULT_RECALL_OPTIONS);
  });
});

