import { describe, expect, it, vi } from "vitest";
import { BANK_PATH_PREFIX, MemoryClient, textsFromRecall } from "../../src/runtime/memory-client";
import type { HookConnection } from "../../src/runtime/hook-connection";

/**
 * The memory-service client the packaged hooks use. It is a client of the
 * service's existing routes only — it introduces no path, status code or JSON
 * field name — and every failure becomes a failure result rather than a throw,
 * so a caller can fail open.
 */

const CONNECTION: HookConnection = { apiUrl: "http://127.0.0.1:18888", bankId: "b-1", apiToken: "tok" };

function client(fetchImpl: typeof fetch, timeoutMs = 1000): MemoryClient {
  return new MemoryClient({ connection: CONNECTION, timeoutMs, fetchImpl });
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}

describe("the bank-scoped call path", () => {
  it("builds the versioned bank-scoped path the service serves", () => {
    const subject = client(async () => jsonResponse({}));
    expect(subject.bankPath("/memories/recall")).toBe(`http://127.0.0.1:18888${BANK_PATH_PREFIX}/b-1/memories/recall`);
  });

  it("posts recall to the bank-scoped recall route with the query body", async () => {
    const fetchImpl = vi.fn(async () => jsonResponse({ results: [{ text: "a", id: "1", type: "fact" }] }));
    await client(fetchImpl as unknown as typeof fetch).recall("what do I know");
    const [url, init] = fetchImpl.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe(`http://127.0.0.1:18888${BANK_PATH_PREFIX}/b-1/memories/recall`);
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ query: "what do I know" });
  });

  it("posts capture to the bank-scoped memories route with an items body", async () => {
    const fetchImpl = vi.fn(async () => jsonResponse({ id: "m-1" }));
    await client(fetchImpl as unknown as typeof fetch).capture({ content: "hello" });
    const [url, init] = fetchImpl.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe(`http://127.0.0.1:18888${BANK_PATH_PREFIX}/b-1/memories`);
    expect(JSON.parse(String(init.body))).toEqual({ items: [{ content: "hello" }] });
  });

  it("sends the token as a bearer credential when one is configured", async () => {
    const fetchImpl = vi.fn(async () => jsonResponse({}));
    await client(fetchImpl as unknown as typeof fetch).recall("q");
    const [, init] = fetchImpl.mock.calls[0] as unknown as [string, RequestInit];
    const headers = init.headers as Record<string, string>;
    expect(headers.authorization).toBe("Bearer tok");
  });
});

describe("failing open", () => {
  it("returns a failure result, not a throw, when the service is unreachable", async () => {
    const fetchImpl = vi.fn(async () => {
      throw new Error("connection refused");
    });
    const outcome = await client(fetchImpl as unknown as typeof fetch).recall("q");
    expect(outcome.ok).toBe(false);
    expect(outcome.texts).toEqual([]);
  });

  it("returns a failure result on a non-success status", async () => {
    const fetchImpl = vi.fn(async () => new Response("", { status: 503 }));
    const outcome = await client(fetchImpl as unknown as typeof fetch).recall("q");
    expect(outcome.ok).toBe(false);
    expect(outcome.reason).toContain("503");
  });

  it("returns a failure result when the body will not read", async () => {
    const fetchImpl = vi.fn(async () => new Response("not json", { status: 200 }));
    const outcome = await client(fetchImpl as unknown as typeof fetch).recall("q");
    expect(outcome.ok).toBe(true);
    expect(outcome.texts).toEqual([]);
  });

  it("returns an empty result rather than throwing when nothing was relevant", async () => {
    const fetchImpl = vi.fn(async () => jsonResponse({ results: [] }));
    const outcome = await client(fetchImpl as unknown as typeof fetch).recall("q");
    expect(outcome).toEqual({ ok: true, texts: [] });
  });

  it("reports the status when capture fails", async () => {
    const fetchImpl = vi.fn(async () => new Response("", { status: 500 }));
    const outcome = await client(fetchImpl as unknown as typeof fetch).capture({ content: "x" });
    expect(outcome.ok).toBe(false);
    expect(outcome.reason).toContain("500");
  });
});

describe("reading retrieved texts", () => {
  it("takes the text of each result member", () => {
    expect(textsFromRecall({ results: [{ text: "a", id: "1" }, { text: "b", id: "2" }] })).toEqual(["a", "b"]);
  });

  it("skips empty or non-string texts", () => {
    expect(textsFromRecall({ results: [{ text: "" }, { text: 1 }, {}] })).toEqual([]);
  });

  it("tolerates a malformed body", () => {
    expect(textsFromRecall(null)).toEqual([]);
    expect(textsFromRecall("nope")).toEqual([]);
    expect(textsFromRecall({ results: "no" })).toEqual([]);
  });
});