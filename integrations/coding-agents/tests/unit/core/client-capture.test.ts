import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryClient } from "../../../src/core/client";
import {
  buildPageTrigger,
  KNOWLEDGE_LABELS,
  PAGE_MAX_TOKENS,
  pagesFor,
  pageScopeRule,
  RETAIN_STRATEGIES,
} from "../../../src/core/missions";

afterEach(() => vi.restoreAllMocks());

/** What a client built with bank "repo-a" and no project seeds — the shared-bank clause. */
const PAGES = pagesFor(undefined);

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

describe("MemoryClient.ensureFolder", () => {
  it("returns an existing root folder's id (case-insensitive) and does NOT POST a duplicate", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      {
        match: (m, u) => m === "GET" && u.endsWith("/knowledge-base/tree"),
        json: { roots: [{ id: "existing-1", kind: "folder", name: "initiatives" }] },
      },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    const id = await c.ensureFolder("Initiatives");
    expect(id).toBe("existing-1");
    expect(
      calls.some((k) => k[1] === "POST" && String(k[0]).endsWith("/knowledge-base/folders"))
    ).toBe(false);
  });

  it("creates the folder and returns its new id when the tree has no match", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      { match: (m, u) => m === "GET" && u.endsWith("/knowledge-base/tree"), json: { roots: [] } },
      { match: (m, u) => m === "POST" && u.endsWith("/knowledge-base/folders"), json: { id: "new-folder" } },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    const id = await c.ensureFolder("Initiatives");
    expect(id).toBe("new-folder");
    const post = calls.find((k) => k[1] === "POST" && String(k[0]).endsWith("/knowledge-base/folders"));
    expect(post![2]).toEqual({ name: "Initiatives" });
  });
});

describe("MemoryClient.captureInitiative", () => {
  const routesForNewPage = () => [
    { match: (m: string, u: string) => m === "GET" && u.endsWith("/knowledge-base/tree"), json: { roots: [] } },
    { match: (m: string, u: string) => m === "POST" && u.endsWith("/knowledge-base/folders"), json: { id: "folder-abc" } },
    { match: (m: string, u: string) => m === "POST" && u.endsWith("/knowledge-base/pages"), json: { page_id: "pg" } },
    { match: (m: string, u: string) => m === "POST" && u.endsWith("/memories"), json: { operation_id: "op-1" } },
  ];

  it("names the repository, not the bank, when the client knows one", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, routesForNewPage());
    const c = new MemoryClient({
      apiUrl: "http://x",
      bank: "coding-agent::dotfiles",
      project: "dotfiles",
    });
    await c.captureInitiative({ title: "Retry backoff", summary: "..." });

    const pagePost = calls.find((k) => k[1] === "POST" && String(k[0]).endsWith("/knowledge-base/pages"));
    expect((pagePost![2] as { source_query: string }).source_query).toContain(
      pageScopeRule("dotfiles")
    );
  });

  it("new initiative: POSTs a per-initiative page + a marker retain naming the same page id", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, routesForNewPage());
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    const result = await c.captureInitiative({
      title: "Retry backoff for the uploader",
      summary: "Add exponential backoff so transient upload failures retry.",
    });

    // The returned id is the SERVER-ASSIGNED page id ("pg" from the mock), NOT the derived slug —
    // the pages endpoint mints its own id.
    expect(result).toEqual({ page_id: "pg" });

    const pagePost = calls.find((k) => k[1] === "POST" && String(k[0]).endsWith("/knowledge-base/pages"));
    const pageBody = pagePost![2] as Record<string, unknown>;
    expect(pagePost).toBeDefined();
    expect(pageBody.name).toBe("Retry backoff for the uploader");
    expect(pageBody.parent_id).toBe("folder-abc");
    expect(pageBody.tags).toEqual(["knowledge:feature-work"]);
    // Same subject scoping and budget as a seeded page: the bank also holds facts about the
    // dependencies this repo merely uses. No project was given, so it gets the shared-bank clause.
    expect(pageBody.source_query).toContain('Summarize the "Retry backoff for the uploader"');
    expect(pageBody.source_query).toContain(pageScopeRule());
    expect(pageBody.max_tokens).toBe(PAGE_MAX_TOKENS);

    // Marker retain POST to /memories. The page id rides on the metadata and on the context —
    // NEVER on a tag: tags are matched with exact set-ops against a fixed vocabulary, and one
    // more tag value per initiative pollutes every fact.
    const memPost = calls.find((k) => k[1] === "POST" && String(k[0]).endsWith("/memories"));
    const memBody = memPost![2] as { items: Record<string, unknown>[]; async: boolean };
    const item = memBody.items[0]!;
    expect(item.tags).toEqual(["knowledge:feature-work"]);
    expect(item.metadata).toEqual({ relatedPageId: "pg" });
    // The context is the one channel a page synthesis reads back, so this is what lets the
    // overview link to the REAL page id.
    expect(item.context).toBe("initiative marker for [[page:pg]]");
    expect(item.strategy).toBe("document");
    // A marker consolidates into the same single scope as everything else this package writes.
    expect(item.observation_scopes).toBe("shared");
    expect(memBody.async).toBe(true);
    // A stable per-initiative document id (NOT the page id itself), so a recapture upserts.
    expect(item.document_id).toBe("initiative-marker-pg");

    // The returned page id and the id the marker names must be the same (the real page node id).
    expect((item.metadata as { relatedPageId: string }).relatedPageId).toBe(result.page_id);
    expect(item.context).toContain(`[[page:${result.page_id}]]`);
  });

  it("hashes an initiative page's own schedule", async () => {
    const capture = async (title: string) => {
      const calls: unknown[][] = [];
      stubFetchRouted(calls, routesForNewPage());
      const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
      await c.captureInitiative({
        title,
        summary: "...",
        pageTrigger: buildPageTrigger({ pageTriggerType: "cron", pageTriggerCron: "H H * * *" }),
      });
      const pagePost = calls.find((k) => k[1] === "POST" && String(k[0]).endsWith("/knowledge-base/pages"));
      return (pagePost![2] as { trigger: { refresh_cron: string } }).trigger.refresh_cron;
    };

    const one = await capture("Retry backoff for the uploader");
    expect(one).toMatch(/^\d+ \d+ \* \* \*$/);
    expect(await capture("Retry backoff for the uploader")).toBe(one);
    expect(await capture("Typo-tolerant tag matching")).not.toBe(one);
  });

  it("enhancement (relatesToPageId): NO page POST; marker names the existing page id", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      { match: (m: string, u: string) => m === "POST" && u.endsWith("/memories"), json: { operation_id: "op-1" } },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    const result = await c.captureInitiative({
      title: "More backoff tuning",
      summary: "Tweak the jitter window.",
      relatesToPageId: "initiative-x",
    });

    expect(result).toEqual({ page_id: "initiative-x" });

    // No new page created for enhancements, and no folder lookup/creation either.
    expect(calls.some((k) => k[1] === "POST" && String(k[0]).endsWith("/knowledge-base/pages"))).toBe(false);
    expect(calls.some((k) => k[1] === "GET" && String(k[0]).endsWith("/knowledge-base/tree"))).toBe(false);

    const memPost = calls.find((k) => k[1] === "POST" && String(k[0]).endsWith("/memories"));
    const item = (memPost![2] as { items: Record<string, unknown>[] }).items[0]!;
    expect(item.tags).toEqual(["knowledge:feature-work"]);
    expect(item.metadata).toEqual({ relatedPageId: "initiative-x" });
    expect(item.context).toBe("initiative marker for [[page:initiative-x]]");
    expect(item.content).toContain("Update to an existing initiative");
  });

  it("repeated update captures upsert one marker document, not one per call", async () => {
    // Every update capture used to retain a NEW document, so an active initiative grew the bank
    // by one marker per plan change, forever.
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      { match: (m: string, u: string) => m === "POST" && u.endsWith("/memories"), json: { operation_id: "op" } },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    for (const title of ["Backoff tuning", "Backoff tuning, round two"]) {
      await c.captureInitiative({ title, summary: "...", relatesToPageId: "kp-1" });
    }
    const ids = calls
      .filter((k) => k[1] === "POST" && String(k[0]).endsWith("/memories"))
      .map((k) => (k[2] as { items: { document_id: string }[] }).items[0]!.document_id);
    expect(ids).toEqual(["initiative-marker-kp-1-update", "initiative-marker-kp-1-update"]);
  });

  it("applies retain attribution to initiative markers", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      { match: (m: string, u: string) => m === "POST" && u.endsWith("/memories"), json: { operation_id: "op-1" } },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await c.captureInitiative({
      title: "Attributed initiative",
      summary: "Keep its project provenance.",
      relatesToPageId: "initiative-x",
      stamp: {
        tags: ["project:repo-a"],
        metadata: { project: "repo-a", source: "configured" },
      },
    });

    const item = (calls.find((k) => String(k[0]).endsWith("/memories"))![2] as {
      items: Record<string, unknown>[];
    }).items[0]!;
    expect(item.tags).toEqual(["project:repo-a", "knowledge:feature-work"]);
    // The configured attribution survives alongside the page id the marker adds.
    expect(item.metadata).toEqual({
      project: "repo-a",
      source: "configured",
      relatedPageId: "initiative-x",
    });
  });
});

// ---------------------------------------------------------------------------
// configureBank
// ---------------------------------------------------------------------------
describe("MemoryClient.configureBank", () => {
  it("POSTs the bank manifest to /import, then seeds pages via the knowledge base", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      { match: (m, u) => m === "GET" && u.endsWith("/knowledge-base/tree"), json: { roots: [] } },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await c.configureBank();

    const importPosts = calls.filter((k) => k[1] === "POST" && String(k[0]).endsWith("/import"));
    expect(importPosts).toHaveLength(1);
    // Config before the knowledge base — page seeding needs the bank to exist. (The very first
    // call is the GET /config probe that decides whether the missions are still ours to seed.)
    expect(calls.indexOf(importPosts[0]!)).toBeLessThan(
      calls.findIndex((k) => String(k[0]).includes("/knowledge-base/"))
    );

    const body = importPosts[0]![2] as { version: string; bank: Record<string, unknown> };
    expect(body.version).toBe("1");
    expect(typeof body.bank.reflect_mission).toBe("string");
    expect(body.bank.retain_default_strategy).toBe("git");
    expect(Object.keys(body.bank.retain_strategies as object)).toEqual(
      expect.arrayContaining(["git", "gitlog", "conversation", "document"])
    );
    expect(body.bank.entity_labels).toEqual(
      expect.arrayContaining([expect.objectContaining({ key: "knowledge", tag: true })])
    );
    expect(body.bank.entities_allow_free_form).toBe(true);

    // The manifest must NOT carry pages: a bare mental-model row would have no knowledge-base
    // node, invisible to page search and unreadable by node id.
    expect(body).not.toHaveProperty("mental_models");
    expect(
      calls.filter((k) => k[1] === "POST" && String(k[0]).endsWith("/knowledge-base/pages"))
    ).toHaveLength(PAGES.length);
  });

  it("configureBank({reset: true}) DELETEs the bank before the import POST", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      { match: (m, u) => m === "GET" && u.endsWith("/knowledge-base/tree"), json: { roots: [] } },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await c.configureBank({ reset: true });

    expect(calls[0]![1]).toBe("DELETE");
    expect(String(calls[0]![0]).endsWith("/import")).toBe(false);
    expect(calls[1]![1]).toBe("POST");
    expect(String(calls[1]![0]).endsWith("/import")).toBe(true);
  });

  const routes = (overrides: Record<string, unknown> | undefined, ok = true) => [
    { match: (m: string, u: string) => m === "GET" && u.endsWith("/knowledge-base/tree"), json: { roots: [] } },
    { match: (m: string, u: string) => m === "GET" && u.endsWith("/config"), json: { bank_id: "repo-a", config: {}, overrides }, ok },
  ];

  const run = async (calls: unknown[][], routeList: Route[]) => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string | URL, init?: RequestInit) => {
        const method = init?.method ?? "GET";
        calls.push([String(url), method, init?.body ? JSON.parse(String(init.body)) : undefined]);
        const route = routeList.find((r) => r.match(method, String(url)));
        const okStatus = (route as { ok?: boolean } | undefined)?.ok !== false;
        return okStatus
          ? jsonResponse(200, route?.json ?? { ok: true })
          : jsonResponse(404, (route?.json as object) ?? { detail: "nope" });
      })
    );
    await new MemoryClient({ apiUrl: "http://x", bank: "repo-a" }).configureBank();
    return calls.find((k) => k[1] === "POST" && String(k[0]).endsWith("/import"))?.[2] as
      | { bank: Record<string, unknown> }
      | undefined;
  };

  it("seeds the full template — missions included — on a bank with no mission overrides", async () => {
    const body = await run([], routes({}));
    expect(typeof body!.bank.reflect_mission).toBe("string");
    expect(body!.bank.retain_strategies).toBeDefined();
  });

  it("leaves the missions alone once the bank carries its own", async () => {
    // The user rewrote reflect_mission in the control plane; a later seed pass must not stamp the
    // default back over it.
    const body = await run([], routes({ reflect_mission: "MY OWN MISSION" }));
    expect(body!.bank.reflect_mission).toBeUndefined();
    expect(body!.bank.retain_mission).toBeUndefined();
    expect(body!.bank.observations_mission).toBeUndefined();
  });

  it("still adds the strategies and labels the package writes through", async () => {
    // Not preferences: without "conversation" the session write-back silently extracts under the
    // bank's own config (the server warns on an unknown strategy, it does not reject).
    const body = await run([], routes({ retain_mission: "mine" }));
    expect(Object.keys(body!.bank.retain_strategies as object)).toEqual(
      expect.arrayContaining(["git", "gitlog", "conversation", "document"])
    );
    expect(body!.bank.entity_labels).toEqual(
      expect.arrayContaining([expect.objectContaining({ key: "knowledge" })])
    );
  });

  it("treats a blank override as unset", async () => {
    const body = await run([], routes({ reflect_mission: "   " }));
    expect(typeof body!.bank.reflect_mission).toBe("string");
  });

  it("seeds when the bank-config API is unavailable — nothing to preserve there", async () => {
    // With that API off a user cannot set per-bank missions at all, so there is no edit to protect.
    const body = await run([], routes(undefined, false));
    expect(typeof body!.bank.reflect_mission).toBe("string");
  });

  it("makes no import call at all once the bank carries the whole structure", async () => {
    // The settled steady state: on a bank that already has everything there is nothing left to
    // write. Not a harmless no-op POST — an import of the strategies IS the overwrite, since the
    // server replaces the whole map.
    const calls: unknown[][] = [];
    await run(
      calls,
      routes({
        reflect_mission: "mine",
        retain_default_strategy: "git",
        entities_allow_free_form: true,
        retain_strategies: RETAIN_STRATEGIES,
        entity_labels: [KNOWLEDGE_LABELS],
      })
    );
    expect(calls.some((k) => k[1] === "POST" && String(k[0]).endsWith("/import"))).toBe(false);
    // Pages are seeded either way — they are not bank configuration.
    expect(calls.some((k) => String(k[0]).includes("/knowledge-base/"))).toBe(true);
  });

  it("manageBankConfig: false keeps the package out of the bank's config entirely", async () => {
    const calls: unknown[][] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string | URL, init?: RequestInit) => {
        calls.push([String(url), init?.method ?? "GET", undefined]);
        return jsonResponse(200, { roots: [] });
      })
    );
    await new MemoryClient({ apiUrl: "http://x", bank: "repo-a" }).configureBank({ manage: false });
    expect(calls.some((k) => String(k[0]).endsWith("/import"))).toBe(false);
    // Not even the probe: the bank's configuration is none of this package's business.
    expect(calls.some((k) => String(k[0]).endsWith("/config"))).toBe(false);
    expect(calls.some((k) => String(k[0]).includes("/knowledge-base/"))).toBe(true);
  });

  it("writes defaultBankConfig into the import where the bank is silent", async () => {
    // A bank created by this package is otherwise born with the server's defaults for everything
    // the template does not name; the cheap baseline has to travel in the SAME import that
    // creates it.
    const calls: unknown[][] = [];
    const routeList = routes({ reflect_mission: "seeded", enable_auto_consolidation: true });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string | URL, init?: RequestInit) => {
        const method = init?.method ?? "GET";
        calls.push([String(url), method, init?.body ? JSON.parse(String(init.body)) : undefined]);
        const route = routeList.find((r) => r.match(method, String(url)));
        return jsonResponse(200, route?.json ?? { ok: true });
      })
    );
    await new MemoryClient({ apiUrl: "http://x", bank: "repo-a" }).configureBank({
      defaults: {
        enable_auto_consolidation: false,
        mental_model_min_refresh_interval_seconds: 21600,
      },
    });
    const body = calls.find((k) => k[1] === "POST" && String(k[0]).endsWith("/import"))![2] as {
      bank: Record<string, unknown>;
    };
    expect(body.bank.mental_model_min_refresh_interval_seconds).toBe(21600);
    // The operator's own choice on this bank is not this package's to revert.
    expect(body.bank).not.toHaveProperty("enable_auto_consolidation");
  });

  it("re-seeds the missions after an explicit reset", async () => {
    const calls: unknown[][] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string | URL, init?: RequestInit) => {
        calls.push([String(url), init?.method ?? "GET", init?.body ? JSON.parse(String(init.body)) : undefined]);
        return jsonResponse(200, { roots: [] });
      })
    );
    await new MemoryClient({ apiUrl: "http://x", bank: "repo-a" }).configureBank({ reset: true });
    const body = calls.find((k) => k[1] === "POST" && String(k[0]).endsWith("/import"))![2] as {
      bank: Record<string, unknown>;
    };
    expect(typeof body.bank.reflect_mission).toBe("string");
    // reset deletes the bank, so there is nothing to probe
    expect(calls.some((k) => k[1] === "GET" && String(k[0]).endsWith("/config"))).toBe(false);
  });
});

