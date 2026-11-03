/**
 * Observation scoping and page-search width — the two retrieval-shaping numbers config and the
 * HTTP client must agree on, so they live here where both can import them.
 */

/**
 * How consolidation scopes the observations a retained memory feeds (`observation_scopes` on the
 * retain API). The scalar modes are the service's; a `string[][]` declares the scopes explicitly.
 */
export type ObservationScopes =
  | "shared"
  | "combined"
  | "per_tag"
  | "all_combinations"
  | "per_source"
  | string[][];

/**
 * One global scope per bank: every agent working a repo builds ONE set of beliefs. `combined`
 * (the service's own default of one scope per distinct tag set) fragments that by whatever tags a
 * session happened to carry.
 */
export const DEFAULT_OBSERVATION_SCOPES: ObservationScopes = "shared";

/** Knowledge pages ONE search returns. Lives here so config, client and tools cannot drift. */
export const DEFAULT_PAGE_SEARCH_LIMIT = 10;
