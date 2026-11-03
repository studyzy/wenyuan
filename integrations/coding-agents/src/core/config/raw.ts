/**
 * The config file's shape.
 *
 * ONE file: ~/.wenyuan/config.json (the installer's own store; the installer seeds the connection
 * keys, the user adds everything else around them).
 *
 * Layering, later wins per field:
 *   1. built-in defaults
 *   2. environment variables (WENYUAN_AGENTS_*, see layers.ts) — a FALLBACK for containers, CI
 *      and secret managers
 *   3. the config file's top level
 *   4. its `harnesses.<name>` section for the asking harness
 *
 * There is deliberately NO project-local config: a second, repo-carried file is both a security
 * surface (untrusted repos steering memory behavior) and a second place to look. Per-repo bank
 * routing is `mapPathToBank`; per-agent differences are `harnesses.<name>`.
 */
import type { CustomPagesConfig, PagesConfig, RetainExtractionMode } from "../missions";
import type { ObservationScopes } from "./scopes";

/** The file's shape — every field optional; omitted fields take the documented default. */
export interface RawConfig {
  /** Where memory lives: the wenyuan service's base URL.
   *  Omitted, it is the deployment default (see DEFAULT_API_URL in src/config/connection.ts). */
  apiUrl?: string;
  /** Bearer token (optional). */
  apiToken?: string;
  /** EXPLICIT memory bank id — set = static bank; unset = per-repo dynamic (core/bank.ts). */
  bankId?: string;
  /** Force dynamic resolution even when bankId is set (default: dynamic iff no bankId). */
  dynamicBankId?: boolean;
  /** Dynamic bank id format (default "coding-agent::{gitProject}") — placeholders:
   *  {gitProject} {project} {harness} {channel} {user} (see core/bank.ts). */
  bankIdTemplate?: string;
  /** Absolute path -> bank; longest prefix wins; overrides everything. */
  mapPathToBank?: Record<string, string>;
  /** {gitProject}: worktrees share the main repo's bank (default true). */
  resolveWorktrees?: boolean;
  /** Run memory ONLY in projects that were opted in (default false: every project gets memory,
   *  which is what makes the plugin zero-setup). With it on, an unlisted project is inert — no
   *  bank is created, nothing is retained, no seed runs — so unrelated work leaves no trace. */
  optInOnly?: boolean;
  /** Directories opted in, matched as PREFIXES with `~` expanded: approving a directory approves
   *  the repos beneath it, and each still gets its own dynamic bank. A `mapPathToBank` entry
   *  counts as opted in too, since routing a path to a named bank already declares that project. */
  optInPaths?: string[];
  /** Runtime adapter (default "opencode"). */
  harness?: string;
  /** Hard off-switch — inert plugin, for a no-memory baseline (default false). */
  disabled?: boolean;
  /** Session transcript write-back (default true; set false to opt out). Honored by every
   *  harness. Gates ONLY the transcript — recall, git ingest, seeding and the memory tools keep
   *  working (that is `disabled`'s job). */
  retainSessions?: boolean;
  /** Cap on concurrent retain-related requests the client sends to the API (default 10):
   *  drain()'s per-operation polls and deepen's chat/git retain pools. A single request returning
   *  200 while bursts get 429s means the server is rate-limiting concurrency, not total volume —
   *  lower this rather than raising it. */
  maxParallelRetains?: number;
  /** Automatic session-reflect timeout (default 20000). The default, with the fallback chain
   *  after it, fits the 30s prompt-hook timeout the installer registers on hook harnesses; going
   *  higher there needs that host timeout raised too, or the host kills the hook mid-reflect. */
  reflectTimeoutMs?: number;
  /** Timeout for the agent-invoked reflect tool (default 330000). Deliberately its own knob and
   *  much larger than `reflectTimeoutMs`: that one bounds an automatic hook that must fit the
   *  host's hook window, whereas this one bounds a call the agent made on purpose and waits on,
   *  whose high-budget synthesis on a populated bank can run for minutes. The default sits ABOVE
   *  the server's own reflect wall timeout (300s) so the server decides when to give up, not an
   *  arbitrary client deadline. Unset, it inherits an explicitly-raised `reflectTimeoutMs` — a
   *  user who raised that meant "let reflect run". */
  reflectToolTimeoutMs?: number;
  /** Retrieval timeout for autoInject "pages"/"recall" (default 7000), and the shared budget for
   *  the page-search -> recall fallback after a reflect timeout/5xx. On hook harnesses, the full
   *  reflect + fallback must fit the host's prompt-hook timeout (30s by default). */
  injectTimeoutMs?: number;
  /** Reflect budget for the agent-invoked reflect tool: "low" | "mid" | "high" (default "high").
   *  Drop to "mid"/"low" on a large bank where high-budget synthesis exceeds the server's wall
   *  timeout. The automatic session-start reflect is NOT affected — it always uses "low" to fit
   *  its hook window. */
  reflectBudget?: "low" | "mid" | "high";
  /** What to inject on the session's first prompt (default "reflect"):
   *    "reflect" — one low-budget reflect synthesis (falls back to pages, then recall, on timeout/5xx)
   *    "pages"   — the knowledge pages matching the prompt by search (retrieval only, no LLM)
   *    "recall"  — the bank's memories recalled for the prompt (`recallOptions`; no LLM)
   *    "none"    — nothing; the tool guide routes new goals through pages before optional reflection */
  autoInject?: AutoInject;
  /** @deprecated Use `autoInject`. Still honoured: false = `autoInject: "none"`, true = "reflect";
   *  ignored when `autoInject` is set. Setting it logs a deprecation warning. */
  autoReflect?: boolean;
  /** Knowledge pages ONE search returns (default DEFAULT_PAGE_SEARCH_LIMIT). Applies wherever the
   *  bank is searched: the `autoInject: "pages"` injection, the reflect fallback, and the search
   *  tool — the limit lives on the client side of the fence so they can't drift. */
  pageSearchLimit?: number;
  /** Recall-request overrides, merged key-by-key into the body every recall sends (the
   *  `autoInject: "recall"` source and the reflect fallback) — `{"types": ["world",
   *  "experience"], "max_tokens": 4000}`. Keys are the API's own recall parameters, passed
   *  through unchanged, so a parameter the API gains needs no new setting here; `query` is the
   *  one field a config cannot replace. Defaults to {} at this layer — the full defaults live on
   *  the client, which merges them under these overrides.
   *
   *  A bank with consolidation disabled never grows observations, and the default recall comes
   *  back empty on it: those set `{"types": ["world", "experience"]}`, or `{"types": null}` for
   *  every type. File-only — a nested object does not flatten into an env var. */
  recallOptions?: Record<string, unknown>;
  /** Your own guidance for how the agent should treat memory, added AFTER the built-in tool guide
   *  and after the crediting note that comes back with knowledge-page results. It adds to the
   *  built-in text, never replaces it, so the plugin's call triggers and crediting rule keep up
   *  with releases. */
  toolGuideExtra?: string;
  /** Knowledge-page refresh cadence in user turns (default 1; 0 also means 1). */
  pageRefreshEveryTurns?: number;
  /** What it COSTS to keep this project's knowledge pages current — the trigger stamped on every
   *  page this plugin creates (the seeded taxonomy and each captured initiative):
   *    "cron" (default) — refresh on `pageTriggerCron` only, and only when actually stale
   *    "auto-refresh"   — refresh after every consolidation that produced new material
   *    "manual"         — never refresh on its own; the tools and control plane still can
   *  Auto-refresh is both the most current and the most expensive: one LLM synthesis per page per
   *  consolidation, which adds up fast across auto-surveyed repos — hence the hourly staggered
   *  schedule as the default. Existing pages keep the trigger they were created with — this
   *  changes what NEW pages get. */
  pageTriggerType?: "auto-refresh" | "cron" | "manual";
  /** Schedule for `pageTriggerType: "cron"` — UTC, standard 5-field cron, e.g. "0 3 * * *".
   *  Defaults to DEFAULT_PAGE_TRIGGER_CRON ("H * * * *": hourly, on each page's own minute).
   *  A field written `H` is replaced per page by a value hashed from bank + page name, so pages
   *  spread across the period instead of all firing on the one minute this shared setting names
   *  (see `expandCronHash` in core/missions.ts). */
  pageTriggerCron?: string;
  /** Per-page configuration for the seeded knowledge pages, keyed by page name (case-insensitive):
   *    "Component map": false                                  — don't seed this page at all
   *    "Key decisions and rationale": {"source_query": "..."}   — seed it, asking your question
   *  Omitted (the default) seeds every taxonomy page with its built-in query.
   *
   *  This is the supported way to own a page's query. The plugin re-syncs a page whose live query
   *  differs from the one it is configured to have, so a query edited through the API or the
   *  control plane is replaced on the next session; setting it here makes your wording the
   *  configured one, and the re-sync then keeps it.
   *
   *  A disabled page is NOT deleted — one already seeded keeps its content and simply stops being
   *  re-synced. The subject-scoping clause is appended to a custom query too, so a reworded page
   *  cannot start reporting a dependency's decisions as this project's own.
   *  File-only. In a `banks.<id>` section it REPLACES the global map rather than merging into it. */
  pages?: PagesConfig;
  /** Knowledge pages of your OWN, seeded alongside the taxonomy and keyed by the name they get:
   *    "Security posture": {"source_query": "...", "tags": ["knowledge:decision"]}
   *  `source_query` is the question the page answers. `tags` picks which facts feed it and is
   *  optional — the trigger matches tags with `all`, so omitting them means no tag constraint and
   *  the page synthesizes from everything the bank holds, rather than from nothing.
   *
   *  Deliberately a SEPARATE key from `pages`, not another shape inside it: `pages` rejects a name
   *  that matches no seeded page, which is what turns a typo into a warning instead of silence. If
   *  an unknown key there meant "create a page", a typo would quietly create an empty second page
   *  instead of rewording the one that was meant. These are re-synced like the seeded ones — the
   *  config is the source of truth for the query. A page you create yourself in the control plane
   *  is a different thing entirely and is never touched. File-only, replaced (not merged) by a
   *  `banks.<id>` section, exactly like `pages`. */
  customPages?: CustomPagesConfig;
  /** Session start: auto-seed a cold repo's bank from git history (default true). */
  autoSeed?: boolean;
  /** Session-start auto-seed: most-recent-N-commits cap (default 300). */
  seedLimit?: number;
  /** Session start: spawn a headless agent to survey a cold repo's structure (default true). */
  codebaseSurvey?: boolean;
  /** Model passed to the headless survey (default "haiku"). */
  surveyModel?: string;
  /** Spend cap passed to the headless survey (default 2 USD). */
  surveyBudgetUsd?: number;
  /** Re-run the survey at session start once this many commits have accrued since the last one,
   *  so structural pages track an evolving architecture (default 20; 0 = cold-seed only). */
  surveyRefreshCommits?: number;
  /** Plugin log verbosity ("debug" | "info" | "warn" | "error", default "info");
   *  WENYUAN_AGENTS_LOG_LEVEL overrides for ad-hoc debugging. */
  logLevel?: "debug" | "info" | "warn" | "error";
  /** Keep the installed runtime current by itself (default true). Once a day a session start asks
   *  for the published version and, when newer, re-stages the runtime in the background — the copy
   *  every wired agent's hooks already point at. It rewires no host config, so a release that adds
   *  a NEW hook entry point still needs a manual `install`. Set false to pin the installed version
   *  (air-gapped machines, or a deliberate downgrade). */
  autoUpdate?: boolean;
  /** How git history feeds memory — seeding AND keeping current use the same engine:
   *  "message" = commit messages only (cheap aggregated doc, re-upserted when HEAD moves);
   *  "full"    = messages + every recent commit's full diff (progressive batches, newest first);
   *  "none"    = git ingestion off entirely. Default "message" (cheap by default; opt into depth). */
  gitIngest?: "message" | "full" | "none";
  /** Extra tags stamped on every session write-back, e.g. ["project:{gitProject}", "env:work"].
   *  Placeholders: {gitProject} {project} {harness} {bankId} {sessionId} {timestamp} {channel}
   *  {user} (see core/retain-stamp.ts). Mainly for a SHARED bank, where the bank id no longer says
   *  which repo a memory came from. Built-in tags win on conflict. */
  retainTags?: string[];
  /** Extra metadata stamped on every session write-back, e.g. {"repo": "{gitProject}"}. Same
   *  placeholders as retainTags; built-in metadata (harness attribution) wins on conflict. */
  retainMetadata?: Record<string, string>;
  /** The `context` sent with every session write-back (default: the client's own, which names the
   *  user and the agent as the two speakers). Same placeholders as retainTags. Extraction reads
   *  this to decide WHOSE claim a sentence is. */
  retainContext?: string;
  /** Let the plugin shape the bank's own configuration — the retain strategies it writes under,
   *  the `knowledge` entity-label group, and (on a bank that has none) the missions (default true).
   *
   *  Writing is ADDITIVE: the plugin adds what the bank does not already define and never
   *  overwrites an existing value, so an edit made in the control plane survives — except the
   *  extraction mode of its own strategies, which follows `retainExtractionMode`. Set false
   *  to keep it out of the bank's configuration entirely — for a bank you shape yourself, or share
   *  with non-coding work. That bank should then define the strategies this plugin retains under
   *  (`git`, `gitlog`, `conversation`, `document`, `survey`): the server does not reject a retain
   *  naming a strategy the bank lacks, it logs a warning and extracts with the bank's own config
   *  instead. Knowledge pages are seeded either way (see `pageTriggerType`). */
  manageBankConfig?: boolean;
  /** How the server extracts memories from what this plugin retains — sessions, commits, documents
   *  (default "concise"). One of "concise", "verbose", "verbatim", "chunks" (store the text with no
   *  extraction). Every Stop writes the session back, so this is what each turn costs: "verbose"
   *  extracts more detail at several times the tokens. Kept in sync on the plugin's own retain
   *  strategies on every session start, so changing it reaches an existing bank too (unless
   *  `manageBankConfig` is false). Anything else falls back to the default. */
  retainExtractionMode?: RetainExtractionMode;
  /** Bank-config fields of your own for the banks this plugin shapes, e.g.
   *    {"enable_observations": false, "enable_auto_consolidation": false,
   *     "mental_model_min_refresh_interval_seconds": 21600}
   *  Keys are the bank-config API's own field names, passed straight through, so anything the
   *  bank-config accepts is settable here. Written under the same rule as the rest of the
   *  template — only where the bank does not already define the field — so a bank the plugin
   *  creates is born with these instead of the server's defaults, while a value you set in the
   *  control plane is never overwritten. Wins over the template on a key both name. The fields
   *  the plugin governs itself (`retain_strategies`, `entity_labels`, `retain_extraction_mode`)
   *  are refused with a warning: the first two are merged per entry, the last follows
   *  `retainExtractionMode`. Ignored when `manageBankConfig` is false.
   *  File-only. In a `banks.<id>` section it REPLACES the global map rather than merging into it. */
  defaultBankConfig?: Record<string, unknown>;
  /** How consolidation groups the observations this plugin's memories feed (default "shared" —
   *  one global scope per bank, so every agent working a repo builds ONE set of beliefs; see
   *  DEFAULT_OBSERVATION_SCOPES). "combined" restores the server default of one scope per distinct
   *  tag set, "per_tag" one per tag, "all_combinations" one per subset; a string[][] declares the
   *  scopes literally. Anything else falls back to the default. */
  observationScopes?: ObservationScopes;
  /** Per-harness overrides of any of the fields above, keyed by harness name ("opencode",
   *  "claude-code", ...). Lets one config file give each agent its own bank/settings. */
  harnesses?: Record<string, Omit<RawConfig, "harnesses">>;
  /** Per-BANK overrides, applied AFTER bank resolution — the per-repo opt-in/out surface, keyed
   *  by the RESOLVED bank id (run a session once or check the banner to see it). Any behavioral
   *  field, plus `bank` to RENAME the destination (single hop: the section is selected by the
   *  resolved id, the rename is literal — several ids may converge on one shared bank).
   *  Bank-resolution fields (bankId/template/map/...) are ignored here. Example:
   *    "banks": { "coding-agent::secret-client": { "disabled": true },
   *               "coding-agent::old-name": { "bank": "team::shared" },
   *               "coding-agent::big-mono": { "gitIngest": "full", "retainSessions": false } } */
  banks?: Record<string, Omit<RawConfig, "banks" | "harnesses"> & { bank?: string }>;
  /** Per-DIRECTORY overrides, keyed by absolute path prefix (`~` allowed; longest prefix wins;
   *  a linked worktree uses its main checkout's entry). Applied after bank resolution and before
   *  the `banks.<id>` section, so one entry can send every repo under a directory to another
   *  server or tenant while each repo keeps its own bank. Bank-resolution fields are ignored here,
   *  as in `banks`. Example:
   *    "paths": { "~/work/client-x": { "apiToken": "client-x-key" } } */
  paths?: Record<string, Omit<RawConfig, "banks" | "harnesses" | "paths">>;
}

/** Fully-resolved config: every field present. */
export interface Config {
  apiUrl: string;
  apiToken?: string;
  bankId?: string; // resolved per-directory via deriveBankIdOrSkip — see core/bank.ts
  dynamicBankId?: boolean;
  bankIdTemplate?: string;
  mapPathToBank?: Record<string, string>;
  resolveWorktrees?: boolean;
  optInOnly: boolean;
  optInPaths: string[];
  harness: string;
  disabled: boolean;
  retainSessions: boolean;
  maxParallelRetains: number;
  reflectTimeoutMs: number;
  reflectToolTimeoutMs: number;
  injectTimeoutMs: number;
  reflectBudget: "low" | "mid" | "high";
  autoInject: AutoInject;
  pageSearchLimit: number;
  recallOptions: Record<string, unknown>;
  toolGuideExtra?: string;
  pageRefreshEveryTurns: number;
  pageTriggerType: "auto-refresh" | "cron" | "manual";
  pageTriggerCron?: string;
  pages: PagesConfig;
  customPages: CustomPagesConfig;
  autoSeed: boolean;
  seedLimit: number;
  codebaseSurvey: boolean;
  surveyModel: string;
  surveyBudgetUsd: number;
  surveyRefreshCommits: number;
  gitIngest: "message" | "full" | "none";
  retainTags: string[];
  retainMetadata: Record<string, string>;
  retainContext?: string;
  manageBankConfig: boolean;
  retainExtractionMode: RetainExtractionMode;
  defaultBankConfig: Record<string, unknown>;
  observationScopes: ObservationScopes;
  banks: Record<string, Omit<RawConfig, "banks" | "harnesses"> & { bank?: string }>;
  paths: Record<string, Omit<RawConfig, "banks" | "harnesses" | "paths">>;
  logLevel: "debug" | "info" | "warn" | "error";
  autoUpdate: boolean;
}

export type AutoInject = "reflect" | "pages" | "recall" | "none";

export interface LoadOptions {
  /** Which harness is asking ("opencode", "claude-code", ...) — applies its `harnesses.<name>` overrides. */
  harness?: string;
  /** Explicit global-config path (default ~/.wenyuan/config.json). */
  path?: string;
}
