/**
 * Bank detail view router.
 *
 * Selects which view is showing, and which sub-tab within it, from query
 * parameters. Switching pushes new query parameters and never leaves the bank
 * path segment, so the bank stays addressed by its path and a link to a
 * particular tab is a link to that bank.
 *
 * The parameter names and their value sets are URL contract: an existing link
 * or bookmark that names a tab has to keep resolving to that tab.
 */
export const VIEWS = [
  "home",
  "recall",
  "reflect",
  "data",
  "documents",
  "entities",
  "knowledge",
  "profile",
] as const;

/** The data view's sub-tabs, which split memories by fact type. */
export const DATA_SUBTABS = ["world", "experience", "observations"] as const;

/** The knowledge view's sub-tabs. */
export const KNOWLEDGE_SUBTABS = ["pages", "models"] as const;

/** The profile view's sub-tabs. */
export const CONFIGURATION_SUBTABS = [
  "general",
  "memory-defense",
  "configuration",
  "webhooks",
  "audit-logs",
  "llm-requests",
] as const;

export const DEFAULT_VIEW = "home";
export const DEFAULT_DATA_SUBTAB = "world";
export const DEFAULT_KNOWLEDGE_SUBTAB = "pages";
export const DEFAULT_CONFIGURATION_SUBTAB = "general";

/** Query parameter names. These are contract, not an implementation detail. */
export const VIEW_PARAM = "view";
export const DATA_SUBTAB_PARAM = "subTab";
export const KNOWLEDGE_SUBTAB_PARAM = "knowledgeTab";
export const CONFIGURATION_SUBTAB_PARAM = "bankConfigTab";

export type ViewName = (typeof VIEWS)[number];
export type DataSubTab = (typeof DATA_SUBTABS)[number];
export type KnowledgeSubTab = (typeof KNOWLEDGE_SUBTABS)[number];
export type ConfigurationSubTab = (typeof CONFIGURATION_SUBTABS)[number];

export type BankViewState = {
  view: ViewName;
  dataSubTab: DataSubTab;
  knowledgeSubTab: KnowledgeSubTab;
  configurationSubTab: ConfigurationSubTab;
};

function pick<T extends string>(value: string | null, allowed: readonly T[], fallback: T): T {
  return value !== null && (allowed as readonly string[]).includes(value) ? (value as T) : fallback;
}

/** Read the full view state from a search string, falling back on any unknown value. */
export function parseViewState(search: URLSearchParams): BankViewState {
  return {
    view: pick(search.get(VIEW_PARAM), VIEWS, DEFAULT_VIEW),
    dataSubTab: pick(search.get(DATA_SUBTAB_PARAM), DATA_SUBTABS, DEFAULT_DATA_SUBTAB),
    knowledgeSubTab: pick(
      search.get(KNOWLEDGE_SUBTAB_PARAM),
      KNOWLEDGE_SUBTABS,
      DEFAULT_KNOWLEDGE_SUBTAB,
    ),
    configurationSubTab: pick(
      search.get(CONFIGURATION_SUBTAB_PARAM),
      CONFIGURATION_SUBTABS,
      DEFAULT_CONFIGURATION_SUBTAB,
    ),
  };
}

/**
 * Build the query string for a tab switch.
 *
 * Only the parameters the switch owns are replaced, so a control belonging to
 * one view does not silently drop state belonging to another. The bank path
 * segment is left to the caller.
 */
export function nextViewSearch(
  current: URLSearchParams,
  patch: Partial<Record<"view" | "data" | "knowledge" | "config", string>>,
): string {
  const next = new URLSearchParams(current.toString());
  const apply: Record<string, string | undefined> = {
    [VIEW_PARAM]: patch.view,
    [DATA_SUBTAB_PARAM]: patch.data,
    [KNOWLEDGE_SUBTAB_PARAM]: patch.knowledge,
    [CONFIGURATION_SUBTAB_PARAM]: patch.config,
  };
  for (const [key, value] of Object.entries(apply)) {
    if (value === undefined) continue;
    next.set(key, value);
  }
  const serialized = next.toString();
  return serialized.length > 0 ? `?${serialized}` : "";
}

/** Percent-encode a bank id for the page path segment. */
export function bankDetailPath(bankId: string): string {
  return `/banks/${encodeURIComponent(bankId)}`;
}

/** Kept as an alias so callers that imported the old name keep compiling. */
export { VIEWS as BANK_DETAIL_VIEWS };