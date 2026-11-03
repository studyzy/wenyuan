/**
 * The knowledge-page taxonomy: which pages a coding project gets, what each asks, and how they
 * refresh.
 *
 * A page is a tag-scoped synthesis view over the bank's memories — consolidated from history and
 * past conversations, never mirrored from the current source. The tiers say what KIND of knowledge
 * a fact is (the knowledge: labels above), never WHOSE; the subject-scoping rule below carries that
 * other half.
 */


/** One seeded page: its name, the question it answers, and the tier tag that feeds it. */
export interface KnowledgePage {
  name: string;
  source_query: string;
  tags: string[];
}

/**
 * The subject-scoping clause appended to every query this package creates, naming the subject the
 * page is about.
 *
 * A bank collects everything said IN a repository, not everything said ABOUT it: facts about a
 * dependency the repo merely uses land there too, and attribution tags record where a fact ARRIVED
 * from, never what it is ABOUT. Stating the exclusion in the query is what lets the synthesizer
 * tell the two apart while it still has the fact's text in front of it.
 *
 * "project" is the repository when the bank is one repository's, and undefined otherwise — a bank
 * several repos share has no repo to name, and naming the BANK instead scoped every page to a
 * project no memory was about, leaving them all empty. Either way the clause must be stable for
 * the bank: it is PATCHed onto pages that outlive the session.
 */
export function pageScopeRule(project?: string): string {
  if (project === undefined)
    return (
      ` This bank is shared by several projects: attribute each thing you include to the ` +
      `repository or project its sources name, and group by project where that helps. Leave out ` +
      `facts about external tools, libraries and services those projects merely use, configure, ` +
      `deploy or discuss — they belong to somebody else's codebase — including any commit SHA or ` +
      `identifier from such a dependency's repository.`
    );
  return (
    ` Scope this page to ${project} ITSELF: the bank also holds facts about external tools, ` +
    `libraries and services that ${project} merely uses, configures, deploys or discusses, and ` +
    `those belong to somebody else's codebase. Include something only when its subject is ` +
    `${project}'s own code, configuration or process; when it is about a dependency, leave it out ` +
    `however well-evidenced it looks — including any commit SHA or identifier that belongs to that ` +
    `dependency's repository rather than this one.`
  );
}

/** The taxonomy before scoping — never seeded directly; pagesFor binds it to a subject. */
const PAGE_TAXONOMY: readonly KnowledgePage[] = [
  {
    name: "Component map",
    source_query:
      "From this project's commit history and past discussions, what are the main " +
      "components/modules/subsystems, what is each responsible for, and how do they relate to or " +
      "depend on one another? Describe the structure and responsibilities.",
    tags: ["knowledge:component"],
  },
  {
    name: "Core concepts",
    source_query:
      "What are the core concepts, domain abstractions, and key entities in this project — " +
      "the vocabulary a developer must understand? For each, explain what it represents and its role, " +
      "drawn from how they are introduced and discussed across the history and conversations.",
    tags: ["knowledge:concept"],
  },
  {
    name: "Conventions and patterns",
    source_query:
      "What conventions, idioms, and recurring patterns does this project follow — its " +
      "approach to testing, error handling, naming, structure, and how changes are typically made? " +
      "Describe how THIS project does things, as evidenced across its history and discussions.",
    tags: ["knowledge:convention"],
  },
  {
    name: "Key decisions and rationale",
    source_query:
      "What are the significant technical decisions made in this project and the rationale " +
      "behind them — the durable 'why we do it this way' a developer should know? Summarize the " +
      "decisions and their reasoning from the commit rationales and past conversations.",
    tags: ["knowledge:decision"],
  },
  {
    name: "Initiatives and enhancements",
    source_query:
      "Based on this repository's commit history, what are the major initiatives, features, and " +
      "enhancements the project has worked on? Summarize the themes and notable changes over time. " +
      "When a source memory's context carries a `[[page:<id>]]` link, repeat that link in the " +
      "summary of what it describes, so each initiative links to its detailed page.",
    tags: ["knowledge:feature-work"],
  },
];

/** The seeded pages' names, in taxonomy order — the keys the pages config accepts. */
export const PAGE_NAMES: readonly string[] = PAGE_TAXONOMY.map((page) => page.name);

/**
 * What a config says about ONE seeded page:
 *   false                    — do not seed it at all
 *   { source_query: "..." }  — seed it, but ask this instead of the taxonomy's question
 * An absent entry means the taxonomy's own query.
 */
export type PageOverride = false | { source_query?: string };
export type PagesConfig = Record<string, PageOverride>;

/**
 * A page the user defines, as opposed to "pages", which only reworks the taxonomy above.
 * "tags" is OPTIONAL: the trigger matches tags with "all" (see PAGE_TAGS_MATCH), so no tags means
 * no tag constraint — the page synthesizes from everything the bank holds.
 */
export interface CustomPage {
  source_query: string;
  tags?: string[];
}
export type CustomPagesConfig = Record<string, CustomPage>;

/**
 * The seeded pages for one subject: the taxonomy with "project" named in every query, minus the
 * pages "pages" disables, plus the user's own. A pure function of its arguments, so the text is
 * STABLE for a subject and the seeder PATCHes only on real drift. The scoping clause rides on a
 * custom query too — rewording a question is not a choice to take on the dependency-exclusion too.
 */
export function pagesFor(
  project: string | undefined,
  pages: PagesConfig = {},
  customPages: CustomPagesConfig = {}
): KnowledgePage[] {
  const scope = pageScopeRule(project);
  // Case-insensitive on the same key the seeder matches live pages by, so a config entry and the
  // page it names cannot disagree about which page that is.
  const byName = new Map(
    Object.entries(pages).map(([name, override]) => [name.trim().toLowerCase(), override])
  );
  const out: KnowledgePage[] = [];
  for (const page of PAGE_TAXONOMY) {
    const override = byName.get(page.name.toLowerCase());
    if (override === false) continue;
    out.push({ ...page, source_query: (override?.source_query || page.source_query) + scope });
  }
  // The user's own pages, appended after the taxonomy so a taxonomy page keeps its position —
  // the order the seed log and the page roster read in.
  for (const [name, page] of Object.entries(customPages)) {
    out.push({ name: name.trim(), source_query: page.source_query + scope, tags: page.tags ?? [] });
  }
  return out;
}

/** The synthesis budget shared by every page this package creates, seeded or captured. */
export const PAGE_MAX_TOKENS = 4096;

/** A page trigger, in the API's own shape. */
export interface PageTrigger {
  fact_types: string[];
  /** How the page's own tags filter the memories a refresh reads. */
  tags_match: "any" | "all" | "any_strict" | "all_strict" | "exact";
  refresh_after_consolidation?: boolean;
  /** "null" CLEARS a schedule the page already has — the server only drops the unstated
   *  counterpart of a truthy field, so a falsy false alone would leave a cron firing (see
   *  pageTriggerPatch). */
  refresh_cron?: string | null;
}

/**
 * "all" — AND over the page's tier tag, but INCLUDING untagged memories.
 *
 * The server defaults a tagged model to "all_strict", which excludes untagged rows — and every
 * observation in these banks is untagged (the shared consolidation scope carries no tags). Under
 * "all_strict" a page asking for observations could not retrieve a single one. "all" keeps the
 * discrimination that matters (a knowledge:decision fact still cannot reach the Component map) while
 * letting the untagged shared observations reach every page, where source_query does the selecting.
 */
const PAGE_TAGS_MATCH = "all" as const;

/** A page synthesizes from all three tiers; the fact types are not a preference. */
export const PAGE_FACT_TYPES = ["world", "experience", "observation"];

/**
 * The default schedule: hourly, each page on its own hashed minute (see cron-hash.ts).
 *
 * Hourly rather than daily — a page read at session start is worthless a day stale. Hourly rather
 * than per-consolidation — a refresh costs one synthesis per page and a busy repo consolidates far
 * more often. The server skips a tick with nothing new, so an idle repo pays nothing.
 */
export const DEFAULT_PAGE_TRIGGER_CRON = "H * * * *";

/** The config fields that shape the trigger (a subset of the resolved config). */
export interface PageTriggerConfig {
  pageTriggerType?: "auto-refresh" | "cron" | "manual";
  pageTriggerCron?: string;
}

/** A page's refresh policy as the tree reports it — the EFFECTIVE one, defaults filled in. */
export interface CurrentPageTrigger {
  tags_match?: string;
  refresh_after_consolidation?: boolean;
  refresh_cron?: string | null;
}

/**
 * The trigger as it should be created for this project's pages.
 *
 * WHEN is the only preference here: "cron" on the default schedule stands in for a missing
 * expression, so a partial config never yields a cron trigger with nothing to fire on; "manual"
 * refreshes only when asked. HOW a page refreshes is the server's to default — restating the
 * server's own defaults would freeze a copy that drifts — so this states only what it means.
 */
export function buildPageTrigger(cfg: PageTriggerConfig = {}): PageTrigger {
  const base: PageTrigger = { fact_types: PAGE_FACT_TYPES, tags_match: PAGE_TAGS_MATCH };
  switch (cfg.pageTriggerType) {
    case "auto-refresh":
      return { ...base, refresh_after_consolidation: true };
    case "manual":
      return { ...base, refresh_after_consolidation: false };
    default:
      return { ...base, refresh_cron: cfg.pageTriggerCron || DEFAULT_PAGE_TRIGGER_CRON };
  }
}

/**
 * Has an existing page's refresh policy drifted from what this config asks for? Compared against
 * the page's OWN resolved trigger — under a hashed cron every page has a different expression, and
 * the shared unresolved one would report drift everywhere. Only the fields this package states are
 * compared; the rest of the trigger is the server's or the operator's.
 */
export function pageTriggerDrifted(current: CurrentPageTrigger, desired: PageTrigger): boolean {
  return (
    current.tags_match !== desired.tags_match ||
    (current.refresh_cron ?? null) !== (desired.refresh_cron ?? null) ||
    Boolean(current.refresh_after_consolidation) !== Boolean(desired.refresh_after_consolidation)
  );
}

/**
 * The trigger to PATCH onto an existing page, given the one we would create it with.
 *
 * The server merges a patch field by field and drops the unstated counterpart of a TRUTHY refresh
 * field, so a cron patch clears auto-refresh by itself. "manual" is falsy and clears nothing — an
 * explicit refresh_cron: null closes that gap.
 */
export function pageTriggerPatch(desired: PageTrigger): PageTrigger {
  if (desired.refresh_after_consolidation === false) return { ...desired, refresh_cron: null };
  return desired;
}

/** Re-exported so the label group travels with the page vocabulary it feeds. */
