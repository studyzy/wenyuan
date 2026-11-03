import { afterEach, describe, expect, it, vi } from "vitest";
import {
  MemoryClient,
} from "../../../src/core/client";
import {
  buildPageTrigger,
  pagesFor,
  pageTriggerFor,
} from "../../../src/core/missions";

afterEach(() => vi.restoreAllMocks());

/** What a client built with bank "repo-a" and no project seeds — the shared-bank clause. */
const PAGES = pagesFor(undefined);

/** The trigger a page on bank "repo-a" settles at under the default config — its hashed cron
 *  already resolved, which is what the server would report back for it. */
const settledTrigger = (name: string) => pageTriggerFor(buildPageTrigger(), "repo-a", name);

function jsonResponse(status: number, body: unknown, headers?: Record<string, string>): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ...(headers ?? {}) },
  });
}

type Route = { match: (method: string, url: string) => boolean; json: unknown };

/** Route JSON responses by (method, url-substring) so multi-call flows can return distinct bodies. */
function stubFetchRouted(calls: unknown[][], routes: Route[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string | URL, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      calls.push([String(url), method, init?.body ? JSON.parse(String(init.body)) : undefined]);
      const route = routes.find((r) => r.match(method, String(url)));
      return jsonResponse(200, route?.json ?? { ok: true });
    })
  );
}

// ---------------------------------------------------------------------------
// append / version capability probes
// ---------------------------------------------------------------------------
describe("MemoryClient append capability", () => {
  it("rejects append when the bank cannot store document text", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      { match: (m, u) => m === "GET" && u.endsWith("/version"), json: { api_version: "0.10.1" } },
      {
        match: (m, u) => m === "GET" && u.endsWith("/config"),
        json: { config: { store_document_text: false }, overrides: {} },
      },
    ]);

    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await expect(c.supportsAppendRetain()).resolves.toBe(false);
    expect(calls.map((call) => call[0])).toEqual([
      "http://x/v1/version",
      "http://x/v1/default/banks/repo-a/config",
    ]);
  });

  it("accepts append when the resolved bank config stores document text", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      { match: (m, u) => m === "GET" && u.endsWith("/version"), json: { api_version: "0.10.1" } },
      {
        match: (m, u) => m === "GET" && u.endsWith("/config"),
        json: { config: { store_document_text: true }, overrides: {} },
      },
    ]);
    await expect(
      new MemoryClient({ apiUrl: "http://x", bank: "repo-a" }).supportsAppendRetain()
    ).resolves.toBe(true);
  });

  it("assumes append is supported when the bank config probe fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string | URL) => {
        if (String(url).endsWith("/config")) throw new Error("timeout");
        return jsonResponse(200, { api_version: "0.10.1" });
      })
    );
    await expect(
      new MemoryClient({ apiUrl: "http://x", bank: "repo-a" }).supportsAppendRetain()
    ).resolves.toBe(true);
  });

  it("never appends against a server without idempotent retain, whatever the bank says", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      { match: (m, u) => m === "GET" && u.endsWith("/version"), json: { api_version: "0.1.0" } },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await expect(c.supportsAppendRetain()).resolves.toBe(false);
    expect(calls.map((call) => call[0])).toEqual(["http://x/v1/version"]);
  });

  it("probes /v1/version first, so a /v1-only ingress still enables append", async () => {
    // The bare /version lands on the control plane behind such an ingress: an HTML page.
    const fetchMock = vi.fn(async (url: string | URL) =>
      String(url) === "http://x/v1/version"
        ? jsonResponse(200, { api_version: "0.10.3" })
        : jsonResponse(200, { not: "json" })
    );
    vi.stubGlobal("fetch", fetchMock);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await expect(c.supportsIdempotentRetain()).resolves.toBe(true);
    expect(fetchMock.mock.calls.map((call) => String(call[0]))).toEqual(["http://x/v1/version"]);
  });

  it("falls back to /version on a server that predates the /v1 alias", async () => {
    const fetchMock = vi.fn(async (url: string | URL) =>
      String(url) === "http://x/v1/version"
        ? jsonResponse(404, { detail: "Not Found" })
        : jsonResponse(200, { api_version: "0.9.0" })
    );
    vi.stubGlobal("fetch", fetchMock);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await expect(c.supportsIdempotentRetain()).resolves.toBe(true);
    expect(fetchMock.mock.calls.map((call) => String(call[0]))).toEqual([
      "http://x/v1/version",
      "http://x/version",
    ]);
  });
});

// ---------------------------------------------------------------------------
// knowledge-page reads + the 404 latch
// ---------------------------------------------------------------------------
describe("MemoryClient knowledge-page reads", () => {
  it("recognizes an older server and exposes the missing capability", async () => {
    vi.stubGlobal("fetch", vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(404, { detail: "not found" })));
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await expect(c.listPages()).rejects.toMatchObject({ code: "knowledge_pages_unavailable" });
    expect(c.knowledgePagesSupported).toBe(false);
    await expect(c.searchKnowledgePages("architecture")).rejects.toMatchObject({
      code: "knowledge_pages_unavailable",
    });
  });

  it("a bank-not-found 404 is NOT a missing endpoint: no pages, no latched capability", async () => {
    // The bank is minted by the first retain, so the first session in a new bank ALWAYS reads
    // pages before it exists. Latching there disabled knowledge pages for the whole process.
    let status = 404;
    let detail = "Bank 'repo-a' not found";
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => (status === 404
        ? jsonResponse(404, { detail })
        : jsonResponse(200, { roots: [{ id: "kp-1", kind: "page", name: "Core concepts" }] }))
      )
    );
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    expect(await c.listPages()).toEqual({ items: [] });
    expect(c.knowledgePagesSupported).toBeUndefined();

    // The latch keys on the ENDPOINT-missing body, not the bank error's wording, so rephrasing
    // that message cannot quietly reinstate the bug.
    detail = "No bank named repo-a";
    expect(await c.listPages()).toEqual({ items: [] });
    expect(c.knowledgePagesSupported).toBeUndefined();

    // Search goes through the same check, so it too reports "no pages yet" without latching.
    detail = "Bank 'repo-a' not found";
    expect(await c.searchKnowledgePages("architecture")).toEqual([]);
    expect(c.knowledgePagesSupported).toBeUndefined();

    // Once the bank exists, the very same client sees its pages — no restart needed.
    status = 200;
    expect(await c.listPages()).toEqual({ items: [{ id: "kp-1", name: "Core concepts" }] });
    expect(c.knowledgePagesSupported).toBe(true);
  });

  it("search latches on its OWN endpoint-missing 404 — it is not only reached via listPages", async () => {
    // Without this, search parsed a server's own 404 into zero hits and reported "nothing
    // matched" forever, which reads as an empty bank rather than a missing feature.
    vi.stubGlobal("fetch", vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(404, { detail: "Not Found" })));
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await expect(c.searchKnowledgePages("architecture")).rejects.toMatchObject({
      code: "knowledge_pages_unavailable",
    });
    expect(c.knowledgePagesSupported).toBe(false);
  });

  it("a 404 with no JSON body does NOT latch: a proxy answers it during a backend restart", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        new Response("<html>502</html>", { status: 404, headers: { "Content-Type": "text/html" } })
      )
    );
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    expect(await c.listPages()).toEqual({ items: [] });
    expect(c.knowledgePagesSupported).toBeUndefined();
    expect(await c.searchKnowledgePages("architecture")).toEqual([]);
    expect(c.knowledgePagesSupported).toBeUndefined();
  });

  it("seedPages: a bank that does not exist yet writes nothing and does not latch", async () => {
    const calls: unknown[][] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string | URL, init?: RequestInit) => {
        calls.push([String(url), init?.method ?? "GET", undefined]);
        // The tree is empty (bank missing), then the first create 404s for the same reason.
        return String(url).includes("/knowledge-base/")
          ? jsonResponse(404, { detail: "Bank 'repo-a' not found" })
          : jsonResponse(200, {});
      })
    );
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await expect(c.seedPages()).resolves.toBeUndefined();
    expect(c.knowledgePagesSupported).toBeUndefined();
    // It stops at the first create rather than 404ing once per page.
    expect(calls.filter((x) => x[1] === "POST").length).toBe(1);
  });

  it("seedPages: a page deleted under us is skipped, not the rest of the sync", async () => {
    // This used to return, abandoning every page after the missing one — and the summary line.
    const patched: string[] = [];
    const drifted = PAGES.map((p, i) => ({
      id: `kp-${i}`,
      kind: "page",
      name: p.name,
      description: "stale query", // forces a source_query PATCH on every page
      trigger: settledTrigger(p.name),
    }));
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string | URL, init?: RequestInit) => {
        if (String(url).endsWith("/knowledge-base/tree"))
          return jsonResponse(200, { roots: drifted });
        if (init?.method === "PATCH") {
          patched.push(String(url));
          // The first page has vanished; every later one still patches fine.
          return patched.length === 1
            ? jsonResponse(404, { detail: "Node not found" })
            : jsonResponse(200, {});
        }
        return jsonResponse(200, {});
      })
    );
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await c.seedPages();
    expect(patched.length).toBe(PAGES.length);
    expect(c.knowledgePagesSupported).toBe(true);
  });

  it("listPages reads the knowledge-base tree — never the mental-models surface", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [{ match: (_m, _u) => true, json: { roots: [] } }]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await c.listPages();
    expect(calls).toHaveLength(1);
    expect(calls[0]![1]).toBe("GET");
    expect(String(calls[0]![0])).toContain("/v1/default/banks/repo-a/knowledge-base/tree");
  });

  it("listPages flattens nested pages, drops folders, and keeps the containing folder name", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      {
        match: (_m, _u) => true,
        json: {
          roots: [
            { id: "kp-1", kind: "page", name: "Component map", description: "what are the parts?" },
            {
              id: "kf-1",
              kind: "folder",
              name: "Initiatives",
              children: [{ id: "kp-2", kind: "page", name: "Retry backoff" }],
            },
          ],
        },
      },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    expect(await c.listPages()).toEqual({
      items: [
        { id: "kp-1", name: "Component map", description: "what are the parts?" },
        { id: "kp-2", name: "Retry backoff", folder: "Initiatives" },
      ],
    });
  });

  it("listPages degrades to an empty roster when the tree body isn't JSON", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, _init?: RequestInit) => new Response("not json at all {{{", { status: 200 }))
    );
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    expect(await c.listPages()).toEqual({ items: [] });
  });

  it("getPage GETs knowledge-base/pages/{id} and shapePage collapses body/markdown", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      {
        match: (_m, u) => u.includes("/knowledge-base/pages/"),
        json: {
          id: "p1",
          name: "Pricing decisions",
          description: "What has been decided about pricing?",
          tags: ["type:knowledge-page"],
          timestamp: "2026-09-17T10:00:00Z",
          body: "The threshold is compared against the discounted subtotal.",
          // The API also returns the SAME body with YAML frontmatter on top; passing the response
          // through handed the model the page twice.
          markdown:
            "---\nname: Pricing decisions\n---\nThe threshold is compared against the discounted subtotal.",
        },
      },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    expect(await c.getPage("p1")).toEqual({
      id: "p1",
      name: "Pricing decisions",
      description: "What has been decided about pricing?",
      tags: ["type:knowledge-page"],
      last_updated_at: "2026-09-17T10:00:00Z",
      body: "The threshold is compared against the discounted subtotal.",
    });
    expect(String(calls[0]![0])).toContain("/knowledge-base/pages/p1");
  });

  it("getPage falls back to the full markdown when a page has no body", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(200, { id: "p2", name: "Empty", markdown: "---\nname: Empty\n---\n" }))
    );
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    expect(await c.getPage("p2")).toEqual({
      id: "p2",
      name: "Empty",
      body: "---\nname: Empty\n---\n",
    });
  });

  it("URL-encodes pageId in the getPage suffix", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [{ match: (_m, _u) => true, json: { id: "x" } }]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await c.getPage("p 1/x");
    expect(String(calls[0]![0])).toContain(`/knowledge-base/pages/${encodeURIComponent("p 1/x")}`);
  });

  it("getPage throws on 404 instead of returning the error envelope", async () => {
    vi.stubGlobal("fetch", vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse(404, { detail: "not found" })));
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await expect(c.getPage("missing")).rejects.toThrow("knowledge page not found: missing");
  });

  it("searchKnowledgePages sends the client's pageSearchLimit and shapes the hits", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      {
        match: (_m, u) => u.includes("/knowledge-base/search"),
        json: {
          results: [
            { id: "kp-abc", name: "Component map", source_query: "what are the parts?", snippet: "…", score: 0.5 },
          ],
        },
      },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a", pageSearchLimit: 7 });
    const hits = await c.searchKnowledgePages("components");
    expect(String(calls[0]![0])).toContain("limit=7");
    expect(hits).toEqual([
      { id: "kp-abc", name: "Component map", source_query: "what are the parts?", snippet: "…", score: 0.5 },
    ]);
  });

  it("getPage resolves the id shape searchKnowledgePages hands back", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      {
        match: (_m, u) => u.includes("/knowledge-base/search"),
        json: { results: [{ id: "kp-abc", name: "Component map", snippet: "…", score: 0.5 }] },
      },
      { match: (_m, u) => u.includes("/knowledge-base/pages/"), json: { id: "kp-abc" } },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    const [hit] = await c.searchKnowledgePages("components");
    await c.getPage(hit!.id);
    // The read must land on the page endpoint for the very id search returned.
    expect(String(calls[1]![0])).toContain("/knowledge-base/pages/kp-abc");
  });
});

// ---------------------------------------------------------------------------
// seedPages
// ---------------------------------------------------------------------------