/**
 * The knowledge-page surface: read the tree and single pages, search, and keep the seeded page
 * set in sync with its configuration.
 */

import {
  type CustomPagesConfig,
  PAGE_MAX_TOKENS,
  type PagesConfig,
  pageScopeRule,
  type PageTrigger,
  pageTriggerDrifted,
  pageTriggerFor,
  pageTriggerPatch,
  pagesFor,
  buildPageTrigger,
} from "../missions";
import type { RetainStamp } from "../retain-stamp";
import { isEndpointMissing, KnowledgePagesUnavailableError } from "./errors";
import type { KnowledgeNode } from "./types";
import type { ClientInternals } from "./transport";
import { retainMemory } from "./retain";

/**
 * What the agent gets back from reading one page.
 *
 * The API returns body AND markdown, where markdown is that same body with YAML frontmatter on
 * top — passing the response straight through handed the model the entire page twice, on every
 * read. timestamp goes out as last_updated_at: the value is the page's last refresh, and a bare
 * "timestamp" beside a page tells the model nothing about whether it is current.
 */
export function shapePage(page: unknown): unknown {
  const p = (page ?? {}) as Record<string, unknown>;
  const body = typeof p.body === "string" && p.body.trim() ? p.body : p.markdown;
  return {
    id: p.id,
    name: p.name,
    ...(p.description ? { description: p.description } : {}),
    ...(Array.isArray(p.tags) && p.tags.length ? { tags: p.tags } : {}),
    ...(p.timestamp ? { last_updated_at: p.timestamp } : {}),
    body,
  };
}

/**
 * Latch knowledgePagesSupported = false iff this response really means the endpoint is absent.
 * Returns whether it latched. A bank-not-found 404 is NOT a capability verdict — it is the
 * expected answer before the bank's first retain — so it must never cache a negative.
 *
 * Only 404 is tested: req throws on every other non-ok status it was not told to tolerate, so
 * a 405/501 never reach a caller in the first place.
 */
async function pagesUnsupported(c: ClientInternals, r: Response): Promise<boolean> {
  if (r.status !== 404 || !(await isEndpointMissing(r))) return false;
  c.knowledgePagesSupported = false;
  return true;
}

/**
 * The bank's knowledge-base tree (folders + pages, nested). The tree carries names, source
 * queries and staleness but NOT synthesized content, so it is cheap enough to poll.
 */
export async function tree(c: ClientInternals): Promise<KnowledgeNode[]> {
  if (c.knowledgePagesSupported === false) throw new KnowledgePagesUnavailableError();
  const r = await c.req("GET", c.bankUrl("/knowledge-base/tree"));
  if (await pagesUnsupported(c, r)) throw new KnowledgePagesUnavailableError();
  // Bank not created yet: it genuinely has no pages, and the capability stays UNKNOWN so the
  // next call (after the first retain mints the bank) asks again instead of short-circuiting.
  if (r.status === 404) return [];
  c.knowledgePagesSupported = true;
  try {
    return ((await r.json()) as { roots?: KnowledgeNode[] }).roots ?? [];
  } catch {
    return [];
  }
}

/**
 * List knowledge pages (ids + names only — no synthesized content), flattened out of the tree
 * and shaped as {items:[...]} for the page-list parsers. Folders are dropped: they carry no
 * content to read, and a caller enumerating pages wants leaves, not structure.
 *
 * NOTE the ids are knowledge-base node ids (kp-...), NOT the backing mental-model ids. Every
 * page read/update in this client goes through the same knowledge-base ids, so a page id from
 * here, from searchKnowledgePages, or from a [[page:<id>]] link all resolve identically.
 */
export async function listPages(c: ClientInternals): Promise<unknown> {
  const items: { id: string; name: string; description?: string; folder?: string }[] = [];
  const walk = (nodes: KnowledgeNode[], folder?: string): void => {
    for (const n of nodes) {
      if (!n?.id || !n?.name) continue;
      if (n.kind === "page") {
        items.push({
          id: n.id,
          name: n.name,
          ...(n.description ? { description: n.description } : {}),
          ...(folder ? { folder } : {}),
        });
      }
      if (n.children?.length) walk(n.children, n.kind === "folder" ? n.name : folder);
    }
  };
  walk(await tree(c));
  return { items };
}

/**
 * Read one knowledge page's synthesized content by knowledge-base id, shaped by shapePage so
 * the body arrives once. The endpoint omits the internal reflect trace that built the page —
 * that is most of the raw bytes and can blow past a host's per-tool-result token cap.
 */
export async function getPage(c: ClientInternals, pageId: string): Promise<unknown> {
  if (c.knowledgePagesSupported === false) throw new KnowledgePagesUnavailableError();
  const r = await c.req(
    "GET",
    c.bankUrl(`/knowledge-base/pages/${encodeURIComponent(pageId)}`)
  );
  if (r.status === 404) throw new Error(`knowledge page not found: ${pageId}`);
  return shapePage(await r.json());
}

/** Hybrid (BM25 + vector, RRF-fused) server-side search over the bank's knowledge pages.
 *  Returns page-level hits with a relevance snippet — the real search behind the agent-facing
 *  knowledge-page search tool. */
export async function searchKnowledgePages(
  c: ClientInternals,
  query: string,
  opts: { limit?: number; timeoutMs?: number } = {}
): Promise<
  { id: string; name: string; source_query?: string; snippet: string; score: number }[]
> {
  if (c.knowledgePagesSupported === false) throw new KnowledgePagesUnavailableError();
  const q = `?q=${encodeURIComponent(query)}&limit=${opts.limit ?? c.pageSearchLimit}`;
  const r = await c.req(
    "GET",
    c.bankUrl(`/knowledge-base/search${q}`),
    undefined,
    [],
    opts.timeoutMs
  );
  // Routed through the same check as every other page endpoint. Without it a server with no
  // knowledge-base API parsed its own 404 body into zero hits and reported "nothing matched"
  // forever, which reads as an empty bank rather than a missing feature.
  if (await pagesUnsupported(c, r)) throw new KnowledgePagesUnavailableError();
  if (r.status === 404) return []; // bank not created yet — no pages to match
  const j = (await r.json()) as {
    results?: {
      id: string;
      name: string;
      source_query?: string | null;
      snippet?: string;
      score?: number;
    }[];
  };
  return (j.results ?? []).map((x) => ({
    id: x.id,
    name: x.name,
    ...(x.source_query ? { source_query: x.source_query } : {}),
    snippet: x.snippet ?? "",
    score: x.score ?? 0,
  }));
}

/**
 * Seed the fixed page taxonomy as knowledge-base pages at the tree root, idempotently.
 *
 * Matched by NAME, not id: the pages endpoint mints its own kp-... id, so a stable client-chosen
 * id is not available to match on. Names are unique per folder server-side, which makes them a
 * sound key.
 *
 * An existing page is PATCHed rather than recreated so an upgrade that rewords a source_query
 * re-syncs onto the live page instead of orphaning its synthesized content — which is how
 * pageScopeRule's repo name reaches banks seeded by an earlier version.
 */
export async function seedPages(
  c: ClientInternals,
  pageTrigger: PageTrigger = buildPageTrigger(),
  pagesConfig: PagesConfig = {},
  customPages: CustomPagesConfig = {}
): Promise<void> {
  // No project = a bank no single repository owns: the shared-bank clause. Pages seeded under an
  // older bank-named clause drift from it and are PATCHed below, which rebuilds them.
  const pages = pagesFor(c.project, pagesConfig, customPages);
  const existing = new Map<string, KnowledgeNode>();
  let roots: KnowledgeNode[];
  try {
    roots = await tree(c);
  } catch (e) {
    if (e instanceof KnowledgePagesUnavailableError) {
      c.log(`[bank] knowledge pages unavailable on ${c.apiUrl}; continuing without pages`);
      return;
    }
    throw e;
  }
  for (const n of roots) {
    if (n.kind === "page" && n.name) existing.set(n.name.toLowerCase(), n);
  }
  let created = 0;
  let updated = 0;
  let vanished = 0; // deleted under us mid-run — neither re-synced nor unchanged
  for (const page of pages) {
    const hit = existing.get(page.name.toLowerCase());
    const body = {
      name: page.name,
      source_query: page.source_query,
      tags: page.tags,
      max_tokens: PAGE_MAX_TOKENS,
      // Resolved HERE, not in buildPageTrigger: a hashed cron (H) needs the page's identity,
      // and one trigger is built per session for all of them.
      trigger: pageTriggerFor(pageTrigger, c.bank, page.name),
    };
    if (!hit) {
      // 409 = another run seeded this name between our tree read and this POST. That is the
      // outcome we wanted anyway, so tolerate it rather than failing the whole run.
      const r = await c.req("POST", c.bankUrl("/knowledge-base/pages"), body, [409]);
      if (await pagesUnsupported(c, r)) {
        c.log(`[bank] knowledge pages unavailable on ${c.apiUrl}; continuing without pages`);
        return;
      }
      if (r.status === 404) {
        c.log(`[bank] ${c.bank} does not exist yet; pages seed on the next session`);
        return;
      }
      if (r.status !== 409) created++;
    } else {
      const sourceDrift = hit.description !== page.source_query;
      // Older servers omit trigger from the tree, so an absent value means unknown rather than
      // drift. Those servers also reject a trigger-only PATCH as an empty update.
      const triggerDrift = hit.trigger != null && pageTriggerDrifted(hit.trigger, body.trigger);
      if (!sourceDrift && !triggerDrift) continue;

      // The name IS the match key, so it cannot drift; the source query and the trigger can.
      // The trigger is re-sent on ANY difference in the policy this package states — the refresh
      // schedule included — because that is the only way a changed default reaches a bank seeded
      // under the old one. The config is therefore the source of truth for these pages: a
      // trigger edited in the control plane is re-synced back on the next session. Servers that
      // do not report a page's trigger leave its policy unknown; source-query drift can still
      // be reconciled safely.
      const patch: { trigger?: PageTrigger; source_query?: string; tags?: string[] } = {};
      if (sourceDrift) {
        patch.source_query = page.source_query;
        patch.tags = page.tags;
        // Say so. This replaces a query edited through the API or the control plane, which
        // used to happen silently and read as the edit never having saved.
        c.log(
          `[bank] re-syncing "${page.name}" to its configured source_query — set ` +
            `pages[${JSON.stringify(page.name)}].source_query to keep your own wording`
        );
      }
      // The page's OWN resolved trigger (a hashed cron differs per page), not the shared one.
      if (triggerDrift) patch.trigger = pageTriggerPatch(body.trigger);
      const r = await c.req(
        "PATCH",
        c.bankUrl(`/knowledge-base/nodes/${encodeURIComponent(hit.id)}`),
        patch
      );
      if (await pagesUnsupported(c, r)) {
        c.log(`[bank] knowledge pages unavailable on ${c.apiUrl}; continuing without pages`);
        return;
      }
      // The node vanished under us (a concurrent delete). Skip it — the remaining pages are
      // still worth syncing, and the run's summary line is still worth logging.
      if (r.status === 404) {
        vanished++;
        continue;
      }
      updated++;
    }
  }
  const initiatives = await resyncInitiativeTriggers(c, roots, pageTrigger);
  c.log(
    `[bank] knowledge pages seeded on ${c.bank} (scoped to ${c.project ?? "the shared bank"}): ` +
      `${created} created, ${updated} re-synced, ` +
      `${pages.length - created - updated - vanished} unchanged` +
      (vanished ? `, ${vanished} deleted under us` : "") +
      (initiatives ? `, ${initiatives} initiative pages re-synced` : "")
  );
}

/**
 * Bring the captured initiative pages onto the same refresh policy as the seeded taxonomy.
 *
 * captureInitiative stamps this very trigger when it creates a page, so on a bank seeded under
 * an older default they carry the same drift — and on a real repo they are most of it: five
 * taxonomy pages against one page per initiative. Their name and source_query are written once,
 * from the initiative's own title, and re-stating either would rebuild a page whose question
 * never changed. The one exception is the scope clause: a page captured while the clause named
 * the BANK is scoped to a project no memory is about and comes out empty, so exactly that clause
 * is swapped for the current one — the rest of the query is kept. The tree read is reused rather
 * than re-fetched.
 */
async function resyncInitiativeTriggers(
  c: ClientInternals,
  roots: KnowledgeNode[],
  pageTrigger: PageTrigger
): Promise<number> {
  const folder = roots.find(
    (n) => n.kind === "folder" && (n.name || "").toLowerCase() === "initiatives"
  );
  const bankScope = pageScopeRule(c.bank);
  let updated = 0;
  for (const page of folder?.children ?? []) {
    if (page.kind !== "page") continue;
    const patch: { trigger?: PageTrigger; source_query?: string } = {};
    const query = page.description?.replace(bankScope, pageScopeRule(c.project));
    if (query !== page.description) patch.source_query = query;
    // No trigger reported = policy unknown, not divergent (an older server).
    if (page.trigger != null) {
      const desired = pageTriggerFor(pageTrigger, c.bank, page.name);
      if (pageTriggerDrifted(page.trigger, desired)) patch.trigger = pageTriggerPatch(desired);
    }
    if (!patch.trigger && !patch.source_query) continue;
    const r = await c.req(
      "PATCH",
      c.bankUrl(`/knowledge-base/nodes/${encodeURIComponent(page.id)}`),
      patch
    );
    // 404 only: req throws on every other non-ok status it was not told to tolerate. No latch
    // here — a node that has gone missing says nothing about the server's capabilities, so skip
    // it rather than abandoning the rest of the re-sync. If it is the BANK that went rather
    // than one node, this costs one wasted PATCH per initiative page — bounded by the folder's
    // size, and the next session re-syncs from scratch anyway.
    if (r.status === 404) continue;
    updated++;
  }
  return updated;
}

/** URL/id-safe slug: lowercase, non-alphanumerics to dashes, trim, cap length; fallback name. */
function slugify(s: string): string {
  return (
    s
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 60) || "initiative"
  );
}

/**
 * Active-path capture: register a major feature as a per-initiative page + a marker memory.
 * New initiative -> creates a page under the Initiatives folder. Update (relatesToPageId) -> no
 * new page; only a marker accruing to the existing page, so an enhancement or a mid-work plan
 * change lands on the initiative it belongs to.
 *
 * The marker carries its page id in two places, neither of them a tag: metadata.relatedPageId
 * for provenance, and a [[page:<id>]] link in the retain CONTEXT, which is the only one of the
 * channels a page synthesis actually reads — reflect's search results strip metadata, and the
 * id has no business in a tag vocabulary that exists to be matched with exact set-ops.
 */
export async function captureInitiative(
  c: ClientInternals,
  args: {
    title: string;
    summary: string;
    relatesToPageId?: string;
    stamp?: RetainStamp;
    /** Same refresh policy as the seeded pages — an initiative page is one of them. */
    pageTrigger?: PageTrigger;
  }
): Promise<{ page_id: string }> {
  // The pages endpoint mints its OWN page id (kp-...); we cannot set it. So for a new initiative
  // we create the page first and adopt the server-assigned id — that id is what the return value
  // and the marker must use, or a read by id 404s and the [[page:<id>]] link points at nothing.
  let pageId = args.relatesToPageId;
  if (!pageId) {
    const folderId = await ensureFolder(c, "Initiatives");
    // Same subject scoping and budget as the seeded pages: an initiative page synthesizes from
    // the same bank, which also holds facts about the dependencies this repo merely uses.
    const r = await c.req(
      "POST",
      c.bankUrl("/knowledge-base/pages"),
      {
        name: args.title,
        source_query:
          `Summarize the "${args.title}" initiative: what is being built or changed and why, ` +
          `and its current state — drawn from the project's memory.` +
          pageScopeRule(c.project),
        parent_id: folderId,
        tags: ["knowledge:feature-work"],
        max_tokens: PAGE_MAX_TOKENS,
        trigger: pageTriggerFor(args.pageTrigger ?? buildPageTrigger(), c.bank, args.title),
      }
    );
    try {
      const j = (await r.json()) as { page_id?: string; id?: string };
      pageId = j.page_id ?? j.id;
    } catch {
      /* fall through to the slug fallback below */
    }
    pageId ||= `initiative-${slugify(args.title)}`; // last resort if the response lacked an id
  }
  // "Update", not "Enhancement": a recapture is just as often a plan change (scope/goal/why
  // rewritten) as an addition, and this string is what the page synthesis reads.
  const verb = args.relatesToPageId ? "Update to an existing initiative" : "New initiative";
  const content = `${verb}: ${args.title}. ${args.summary}`;
  // A STABLE id per initiative, so a recapture upserts instead of adding one more document every
  // call. Keyed on the page, not the title — a recapture may reword it — and two slots: the
  // creation marker keeps what the initiative set out to do, the update marker holds the latest
  // plan change (a newer update supersedes the older one).
  const markerId = `initiative-marker-${slugify(pageId)}${args.relatesToPageId ? "-update" : ""}`;
  const tags = [...new Set([...(args.stamp?.tags ?? []), "knowledge:feature-work"])];
  // The context rides on every fact extracted from this marker, verbatim, so the Initiatives
  // overview can link an initiative to its own page from what it retrieved.
  const context = `initiative marker for [[page:${pageId}]]`;
  await retainMemory(c, content, context, markerId, tags, "document", {
    metadata: { ...(args.stamp?.metadata ?? {}), relatedPageId: pageId },
  });
  return { page_id: pageId };
}

/** Find a root folder by name (case-insensitive) or create it; returns its id. Fail-open to undefined. */
export async function ensureFolder(c: ClientInternals, name: string): Promise<string | undefined> {
  try {
    const treeResp = (await (await c.req("GET", c.bankUrl("/knowledge-base/tree"))).json()) as {
      roots?: { id?: string; kind?: string; name?: string }[];
    };
    const hit = (treeResp.roots || []).find(
      (n) => n.kind === "folder" && (n.name || "").toLowerCase() === name.toLowerCase()
    );
    if (hit?.id) return hit.id;
  } catch {
    /* fall through to create */
  }
  try {
    const r = await c.req("POST", c.bankUrl("/knowledge-base/folders"), { name });
    return ((await r.json()) as { id?: string }).id;
  } catch {
    return undefined;
  }
}
