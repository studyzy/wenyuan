/**
 * Bank detail view router.
 *
 * Selects the visible view, data sub-tab, knowledge sub-tab, and configuration
 * sub-tab from query parameters and pushes new query parameters when the user
 * switches, preserving the bank path segment.
 */
export const VIEWS = ["overview", "data", "knowledge", "configuration"] as const;
export const BANK_DETAIL_VIEWS = VIEWS;
export const DATA_SUBTABS = ["memories", "documents", "operations", "stats"] as const;
export const KNOWLEDGE_SUBTABS = ["tree", "search", "export"] as const;
export const CONFIGURATION_SUBTABS = ["general", "aliases", "webhooks", "mental-models", "observations"] as const;

export type ViewName = (typeof VIEWS)[number];

export type BankViewState = {
  view: ViewName;
  dataSubTab: (typeof DATA_SUBTABS)[number];
  knowledgeSubTab: (typeof KNOWLEDGE_SUBTABS)[number];
  configurationSubTab: (typeof CONFIGURATION_SUBTABS)[number];
};

export function parseViewState(search: URLSearchParams): BankViewState {
  const view = (search.get("view") ?? "overview") as ViewName;
  return {
    view: VIEWS.includes(view) ? view : "overview",
    dataSubTab: (search.get("data") ?? "memories") as BankViewState["dataSubTab"],
    knowledgeSubTab: (search.get("knowledge") ?? "tree") as BankViewState["knowledgeSubTab"],
    configurationSubTab: (search.get("config") ?? "general") as BankViewState["configurationSubTab"],
  };
}

/**
 * Build the next query string for a sub-tab switch. The bank path segment is
 * preserved by the caller; only query parameters change here.
 */
export function nextViewSearch(
  current: URLSearchParams,
  patch: Partial<Record<"view" | "data" | "knowledge" | "config", string>>,
): string {
  const next = new URLSearchParams(current.toString());
  for (const [key, value] of Object.entries(patch)) {
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