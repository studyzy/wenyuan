import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryClient } from "../../../src/core/client";
import {
  buildPageTrigger,
  PAGE_MAX_TOKENS,
  pagesFor,
  pageScopeRule,
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

describe("MemoryClient.seedPages", () => {
  it("creates every seeded page through /knowledge-base/pages on an empty bank", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      { match: (m, u) => m === "GET" && u.endsWith("/knowledge-base/tree"), json: { roots: [] } },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await c.seedPages();

    const posts = calls.filter(
      (k) => k[1] === "POST" && String(k[0]).endsWith("/knowledge-base/pages")
    );
    expect(posts).toHaveLength(PAGES.length);
    expect(posts.map((p) => (p[2] as { name: string }).name).sort()).toEqual(
      PAGES.map((p) => p.name).sort()
    );
    for (const post of posts) {
      const body = post[2] as Record<string, unknown>;
      const tags = body.tags as string[];
      expect(tags).toHaveLength(1);
      expect(tags[0]).toMatch(
        /^knowledge:(feature-work|decision|convention|component|concept)$/
      );
      expect(body.max_tokens).toBe(PAGE_MAX_TOKENS);
      // The default schedule, with the page's own hashed minute already resolved.
      const trigger = body.trigger as {
        refresh_cron?: string;
        tags_match?: string;
        fact_types?: string[];
        refresh_after_consolidation?: boolean;
      };
      expect(trigger.refresh_after_consolidation).toBeUndefined();
      expect(trigger.refresh_cron).toMatch(/^(?:[0-9]|[1-5][0-9]) \* \* \* \*$/);
      // NOT the server's all_strict default for a tagged model: that excludes untagged memories,
      // and every observation these banks consolidate is untagged.
      expect(trigger.tags_match).toBe("all");
      expect(trigger.fact_types).toContain("observation");
      expect(body.parent_id).toBeUndefined(); // seeded at the tree root
    }
  });

  it("creates a customPages page at the root, with its own tags and the standard page shape", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      { match: (m, u) => m === "GET" && u.endsWith("/knowledge-base/tree"), json: { roots: [] } },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await c.seedPages(buildPageTrigger(), {}, {
      "Security posture": {
        source_query: "what are our security decisions?",
        tags: ["knowledge:decision"],
      },
    });

    const posts = calls.filter((k) => k[1] === "POST" && String(k[0]).endsWith("/knowledge-base/pages"));
    expect(posts).toHaveLength(PAGES.length + 1);
    const custom = posts
      .map((p) => p[2] as Record<string, unknown>)
      .find((b) => b.name === "Security posture")!;
    expect(custom.source_query).toBe("what are our security decisions?" + pageScopeRule());
    expect(custom.tags).toEqual(["knowledge:decision"]);
    // Same shape as a seeded page in every other respect — root, budget, refresh policy.
    expect(custom.parent_id).toBeUndefined();
    expect(custom.max_tokens).toBe(PAGE_MAX_TOKENS);
    expect((custom.trigger as { tags_match?: string }).tags_match).toBe("all");
  });

  it("honours the pages config: a disabled page is never created, a custom query is what is sent", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      { match: (m, u) => m === "GET" && u.endsWith("/knowledge-base/tree"), json: { roots: [] } },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await c.seedPages(buildPageTrigger(), {
      "Component map": false,
      "Key decisions and rationale": { source_query: "what did we decide and why?" },
    });

    const posts = calls
      .filter((k) => k[1] === "POST" && String(k[0]).endsWith("/knowledge-base/pages"))
      .map((p) => p[2] as Record<string, unknown>);
    expect(posts.map((p) => p.name).sort()).toEqual(
      PAGES.filter((p) => p.name !== "Component map").map((p) => p.name).sort()
    );
    const decisions = posts.find((p) => p.name === "Key decisions and rationale")!;
    expect(decisions.source_query).toBe("what did we decide and why?" + pageScopeRule());
  });

  it("skips page writes when the server has no knowledge-pages API", async () => {
    const calls: unknown[][] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string | URL, init?: RequestInit) => {
        calls.push([String(url), init?.method ?? "GET", undefined]);
        // Exactly what the API answers for an unrouted path — the shape that identifies a server
        // without the knowledge-base API, as opposed to a 404 for a bank that does not exist yet.
        return String(url).endsWith("/knowledge-base/tree")
          ? jsonResponse(404, { detail: "Not Found" })
          : jsonResponse(200, {});
      })
    );
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await expect(c.configureBank()).resolves.toBeUndefined();
    expect(c.knowledgePagesSupported).toBe(false);
    expect(calls.some((x) => String(x[0]).endsWith("/import"))).toBe(true);
    expect(calls.some((x) => String(x[0]).endsWith("/knowledge-base/pages"))).toBe(false);
  });

  it("is idempotent: an already-seeded bank issues no writes at all", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      {
        match: (m, u) => m === "GET" && u.endsWith("/knowledge-base/tree"),
        json: {
          roots: PAGES.map((p, i) => ({
            id: `kp-${i}`,
            kind: "page",
            name: p.name,
            description: p.source_query,
            // The EFFECTIVE policy as the server reports it: the page's own resolved schedule,
            // and the auto-refresh it is mutually exclusive with reported at its default.
            trigger: { ...settledTrigger(p.name), refresh_after_consolidation: false },
          })),
        },
      },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await c.seedPages();
    expect(calls).toHaveLength(1); // the tree GET only
    expect(calls.every((k) => k[1] === "GET")).toBe(true);
  });

  it("tolerates a 409 from a concurrent run that seeded the same name first", async () => {
    const calls: unknown[][] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string | URL, init?: RequestInit) => {
        calls.push([String(url), init?.method ?? "GET", undefined]);
        const conflict = init?.method === "POST" && String(url).endsWith("/knowledge-base/pages");
        return conflict ? jsonResponse(409, { detail: "already exists" }) : jsonResponse(200, { roots: [] });
      })
    );
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    // A losing race must not fail the run — the page exists either way.
    await expect(c.seedPages()).resolves.toBeUndefined();
    expect(
      calls.filter((k) => k[1] === "POST" && String(k[0]).endsWith("/knowledge-base/pages"))
    ).toHaveLength(PAGES.length);
  });

  it("matches by name case-insensitively and PATCHes a page whose source query drifted", async () => {
    const calls: unknown[][] = [];
    const drifted = PAGES[0]!;
    stubFetchRouted(calls, [
      {
        match: (m, u) => m === "GET" && u.endsWith("/knowledge-base/tree"),
        json: {
          roots: PAGES.map((p, i) => ({
            id: `kp-${i}`,
            kind: "page",
            name: p.name.toUpperCase(),
            description: p === drifted ? "an older wording of the query" : p.source_query,
            trigger: settledTrigger(p.name),
          })),
        },
      },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await c.seedPages();

    // Case difference alone must NOT look like a missing page.
    expect(calls.some((k) => k[1] === "POST")).toBe(false);
    const patches = calls.filter((k) => k[1] === "PATCH");
    expect(patches).toHaveLength(1);
    expect(String(patches[0]![0])).toContain("/knowledge-base/nodes/kp-0");
    expect(patches[0]![2]).toEqual({ source_query: drifted.source_query, tags: drifted.tags });
  });

  it("re-syncs the refresh trigger onto a page whose query did NOT drift", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      {
        match: (m, u) => m === "GET" && u.endsWith("/knowledge-base/tree"),
        json: {
          roots: PAGES.map((p, i) => ({
            id: `kp-${i}`,
            kind: "page",
            name: p.name,
            description: p.source_query,
            trigger: { tags_match: "all_strict" },
          })),
        },
      },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await c.seedPages();

    const patches = calls.filter((k) => k[1] === "PATCH");
    expect(patches).toHaveLength(PAGES.length);
    for (const [i, patch] of patches.entries()) {
      // ONLY the trigger: sending source_query would schedule a full rebuild of every page on a
      // bank whose question never changed. The cron is the page's OWN resolved one — patching
      // the literal "H * * * *" would reach the server as a cron it cannot parse.
      expect(patch[2]).toEqual({
        trigger: pageTriggerFor(buildPageTrigger(), "repo-a", PAGES[i]!.name),
      });
    }
  });

  it("migrates a bank still on the old auto-refresh default onto the schedule", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      {
        match: (m, u) => m === "GET" && u.endsWith("/knowledge-base/tree"),
        json: {
          roots: PAGES.map((p, i) => ({
            id: `kp-${i}`,
            kind: "page",
            name: p.name,
            description: p.source_query,
            trigger: { tags_match: "all", refresh_after_consolidation: true, refresh_cron: null },
          })),
        },
      },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await c.seedPages();

    const patches = calls.filter((k) => k[1] === "PATCH");
    expect(patches).toHaveLength(PAGES.length);
    for (const [i, patch] of patches.entries()) {
      const body = patch[2] as { trigger: { refresh_cron?: string | null; refresh_after_consolidation?: boolean } };
      expect(body.trigger.refresh_cron).toBe(settledTrigger(PAGES[i]!.name).refresh_cron);
      // Stating the cron is what clears the auto-refresh server-side; the two are exclusive.
      expect(body.trigger.refresh_after_consolidation).toBeUndefined();
      expect((patch[2] as { source_query?: string }).source_query).toBeUndefined();
    }
  });

  it("re-syncs the initiative pages under their folder, trigger only", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      {
        match: (m, u) => m === "GET" && u.endsWith("/knowledge-base/tree"),
        json: {
          roots: [
            ...PAGES.map((p, i) => ({
              id: `kp-${i}`,
              kind: "page",
              name: p.name,
              description: p.source_query,
              trigger: { ...settledTrigger(p.name), refresh_after_consolidation: false },
            })),
            {
              id: "folder-initiatives",
              kind: "folder",
              name: "Initiatives",
              children: [
                {
                  id: "kp-init-1",
                  kind: "page",
                  name: "Typo-tolerant tag matching",
                  description: "Summarize the initiative",
                  trigger: { tags_match: "all", refresh_after_consolidation: true },
                },
                {
                  // Already settled — an idempotent run must not touch it.
                  id: "kp-init-2",
                  kind: "page",
                  name: "another harness support",
                  description: "Summarize the initiative",
                  trigger: {
                    ...settledTrigger("another harness support"),
                    refresh_after_consolidation: false,
                  },
                },
              ],
            },
          ],
        },
      },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await c.seedPages();

    const patches = calls.filter((k) => k[1] === "PATCH");
    expect(patches).toHaveLength(1);
    expect(String(patches[0]![0])).toContain("kp-init-1");
    // The initiative's own question is left alone — it was written from the title at capture time,
    // and re-stating it would rebuild a page whose question never changed.
    expect(patches[0]![2]).toEqual({
      trigger: pageTriggerFor(buildPageTrigger(), "repo-a", "Typo-tolerant tag matching"),
    });
  });

  it("an initiative page deleted under us is skipped, not the rest of the folder", async () => {
    // This used to break out of the loop, so one vanished page abandoned every initiative after it.
    const drifted = (id: string, name: string) => ({
      id,
      kind: "page",
      name,
      description: "Summarize the initiative",
      trigger: { tags_match: "all", refresh_after_consolidation: true },
    });
    const patched: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string | URL, init?: RequestInit) => {
        if (String(url).endsWith("/knowledge-base/tree"))
          return jsonResponse(200, {
            roots: [
              ...PAGES.map((p, i) => ({
                id: `kp-${i}`,
                kind: "page",
                name: p.name,
                description: p.source_query,
                trigger: settledTrigger(p.name),
              })),
              {
                id: "folder-initiatives",
                kind: "folder",
                name: "Initiatives",
                children: [drifted("kp-init-1", "Gone"), drifted("kp-init-2", "Still here")],
              },
            ],
          });
        if (init?.method === "PATCH") {
          patched.push(String(url));
          return patched.length === 1
            ? jsonResponse(404, { detail: "Node not found" })
            : jsonResponse(200, {});
        }
        return jsonResponse(200, {});
      })
    );
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await c.seedPages();
    expect(patched).toHaveLength(2);
    expect(patched[1]).toContain("kp-init-2");
    expect(c.knowledgePagesSupported).toBe(true);
  });

  it("clears an existing schedule when the config asks for manual refreshes", async () => {
    // "manual" is the one policy whose refresh field is falsy, so the server drops no counterpart:
    // without an explicit null the page would keep firing on the cron it already had.
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      {
        match: (m, u) => m === "GET" && u.endsWith("/knowledge-base/tree"),
        json: {
          roots: PAGES.map((p, i) => ({
            id: `kp-${i}`,
            kind: "page",
            name: p.name,
            description: p.source_query,
            trigger: { ...settledTrigger(p.name), refresh_after_consolidation: false },
          })),
        },
      },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await c.seedPages(buildPageTrigger({ pageTriggerType: "manual" }));

    const patches = calls.filter((k) => k[1] === "PATCH");
    expect(patches).toHaveLength(PAGES.length);
    for (const patch of patches) {
      const body = patch[2] as { trigger: { refresh_after_consolidation?: boolean; refresh_cron?: string | null } };
      expect(body.trigger.refresh_after_consolidation).toBe(false);
      expect(body.trigger.refresh_cron).toBeNull();
    }
  });

  it("does not write when a server does not report a trigger", async () => {
    // A server older than the trigger-in-tree change does not report a page's trigger. That makes
    // the policy unknowable, not divergent; a trigger-only PATCH would be rejected as empty.
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      {
        match: (m, u) => m === "GET" && u.endsWith("/knowledge-base/tree"),
        json: {
          roots: PAGES.map((p, i) => ({
            id: `kp-${i}`,
            kind: "page",
            name: p.name,
            description: p.source_query,
          })),
        },
      },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await c.seedPages();

    expect(calls).toHaveLength(1); // the tree GET only
    expect(calls.every((k) => k[1] === "GET")).toBe(true);
  });

  it("still reconciles source-query drift when the server hides trigger", async () => {
    const calls: unknown[][] = [];
    const drifted = PAGES[0]!;
    stubFetchRouted(calls, [
      {
        match: (m, u) => m === "GET" && u.endsWith("/knowledge-base/tree"),
        json: {
          roots: PAGES.map((p, i) => ({
            id: `kp-${i}`,
            kind: "page",
            name: p.name,
            description: p === drifted ? "an older wording of the query" : p.source_query,
          })),
        },
      },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await c.seedPages();

    const patches = calls.filter((k) => k[1] === "PATCH");
    expect(patches).toHaveLength(1);
    expect(patches[0]![2]).toEqual({
      source_query: drifted.source_query,
      tags: drifted.tags,
    });
  });

  it("gives each page its own slot when the cron asks to be hashed", async () => {
    // The whole point of H: one pageTriggerCron is copied into every repo's config, so a literal
    // expression puts all five pages of every bank on the same minute. Resolution happens at page
    // creation, where the page's identity exists.
    const seed = async (bank: string) => {
      const calls: unknown[][] = [];
      stubFetchRouted(calls, [
        { match: (m, u) => m === "GET" && u.endsWith("/knowledge-base/tree"), json: { roots: [] } },
      ]);
      const c = new MemoryClient({ apiUrl: "http://x", bank });
      await c.seedPages(buildPageTrigger({ pageTriggerType: "cron", pageTriggerCron: "H H * * *" }));
      return calls
        .filter((k) => k[1] === "POST" && String(k[0]).endsWith("/knowledge-base/pages"))
        .map((k) => (k[2] as { trigger: { refresh_cron: string } }).trigger.refresh_cron);
    };

    const a = await seed("repo-a");
    expect(a).toHaveLength(PAGES.length);
    // Resolved to plain cron — H is this package's syntax and the server would reject it.
    for (const cron of a) expect(cron).toMatch(/^\d+ \d+ \* \* \*$/);
    expect(new Set(a).size).toBe(PAGES.length);
    // Stable across runs, and different in another bank seeded from the same config.
    expect(await seed("repo-a")).toEqual(a);
    expect(await seed("repo-b")).not.toEqual(a);
  });

  it("sends a cron with no H exactly as configured", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      { match: (m, u) => m === "GET" && u.endsWith("/knowledge-base/tree"), json: { roots: [] } },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "repo-a" });
    await c.seedPages(buildPageTrigger({ pageTriggerType: "cron", pageTriggerCron: "0 3 * * *" }));
    for (const post of calls.filter((k) => k[1] === "POST")) {
      expect((post[2] as { trigger: { refresh_cron: string } }).trigger.refresh_cron).toBe(
        "0 3 * * *"
      );
    }
  });

  it("a shared bank (no project) gets the shared-bank clause, never one naming the bank", async () => {
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      { match: (m, u) => m === "GET" && u.endsWith("/knowledge-base/tree"), json: { roots: [] } },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "shared-memory" });
    await c.seedPages();

    const posts = calls.filter((k) => k[1] === "POST");
    expect(posts).toHaveLength(5);
    for (const post of posts) {
      expect((post[2] as { source_query: string }).source_query).toContain(pageScopeRule());
      expect(JSON.stringify(post[2])).not.toContain("shared-memory");
    }
  });

  it("repairs pages seeded under the bank-named clause: taxonomy and initiative pages", async () => {
    const stale = pageScopeRule("shared-memory");
    const calls: unknown[][] = [];
    stubFetchRouted(calls, [
      {
        match: (m, u) => m === "GET" && u.endsWith("/knowledge-base/tree"),
        json: {
          roots: [
            ...pagesFor("shared-memory").map((p, i) => ({
              id: `kp-${i}`,
              kind: "page",
              name: p.name,
              description: p.source_query,
            })),
            {
              id: "folder-initiatives",
              kind: "folder",
              name: "Initiatives",
              children: [
                {
                  id: "kp-init-1",
                  kind: "page",
                  name: "Retry backoff",
                  description: `Summarize the "Retry backoff" initiative.${stale}`,
                },
                {
                  // Captured with a repo-owned scope or worded by the user: left alone.
                  id: "kp-init-2",
                  kind: "page",
                  name: "Other",
                  description: `Summarize it.${pageScopeRule("repo-a")}`,
                },
              ],
            },
          ],
        },
      },
    ]);
    const c = new MemoryClient({ apiUrl: "http://x", bank: "shared-memory" });
    await c.seedPages();

    const patches = calls.filter((k) => k[1] === "PATCH");
    expect(patches.map((p) => String(p[0]).split("/").pop())).toEqual([
      "kp-0",
      "kp-1",
      "kp-2",
      "kp-3",
      "kp-4",
      "kp-init-1",
    ]);
    for (const p of patches) expect(JSON.stringify(p[2])).not.toContain(stale);
    expect(patches[5]![2]).toEqual({
      source_query: `Summarize the "Retry backoff" initiative.${pageScopeRule()}`,
    });
  });
});


// ---------------------------------------------------------------------------
// ensureFolder / captureInitiative
// ---------------------------------------------------------------------------