/**
 * Raw config -> resolved config. Pure: this is the single place the defaults live, so an override
 * can be resolved against the CURRENT config and only change what it names.
 */
import { DEFAULT_API_URL } from "../../config/connection";
import {
  type CustomPagesConfig,
  DEFAULT_PAGE_TRIGGER_CRON,
  DEFAULT_RETAIN_EXTRACTION_MODE,
  isHashedCron,
  PAGE_NAMES,
  type PagesConfig,
  parseHashedCron,
  PLUGIN_GOVERNED_BANK_FIELDS,
  RETAIN_EXTRACTION_MODES,
} from "../missions";
import { log } from "../log";
import type { AutoInject, Config, RawConfig } from "./raw";
import { DEFAULT_OBSERVATION_SCOPES, DEFAULT_PAGE_SEARCH_LIMIT, type ObservationScopes } from "./scopes";

/** Default timeout for the automatic hook reflect — see RawConfig.reflectTimeoutMs. */
export const DEFAULT_REFLECT_TIMEOUT_MS = 20_000;
/** Default timeout for the agent-invoked reflect tool — see RawConfig.reflectToolTimeoutMs. */
export const DEFAULT_REFLECT_TOOL_TIMEOUT_MS = 330_000;
/** Default retrieval budget for automatic injection — see RawConfig.injectTimeoutMs. */
export const DEFAULT_INJECT_TIMEOUT_MS = 7_000;

/** Session-start auto-seed commit cap; the seed engine's own default must match. */
export const DEFAULT_SEED_LIMIT = 300;

const AUTO_INJECT_MODES: readonly AutoInject[] = ["reflect", "pages", "recall", "none"];
const REFLECT_BUDGETS = ["low", "mid", "high"] as const;

/** The service's scalar scoping modes; anything else in this field has to be an explicit scope list. */
// `per_source` is this plugin's own mode, resolved per document by the client and never sent to
// the server; the rest are the server's.
const OBSERVATION_SCOPE_MODES = [
  "shared",
  "combined",
  "per_tag",
  "all_combinations",
  "per_source",
] as const;

/** `autoInject` wins; otherwise the deprecated `autoReflect: false` means "none". */
function resolveAutoInject(raw: RawConfig): AutoInject {
  if (raw.autoReflect !== undefined) {
    const replacement = raw.autoReflect === false ? "none" : "reflect";
    log.warn("config", `autoReflect is deprecated — use autoInject: "${replacement}" instead`);
  }
  if (AUTO_INJECT_MODES.includes(raw.autoInject as AutoInject)) return raw.autoInject as AutoInject;
  return raw.autoReflect === false ? "none" : "reflect";
}

/**
 * Which reflect budget the reflect tool should ask for.
 *
 * An unrecognized value takes the default rather than travelling: the API rejects an unknown
 * budget outright, which would turn a typo here into a hard tool failure on every call.
 */
function resolveReflectBudget(raw: RawConfig): "low" | "mid" | "high" {
  const value: unknown = raw.reflectBudget;
  if (value === undefined) return "high";
  if (typeof value === "string" && (REFLECT_BUDGETS as readonly string[]).includes(value))
    return value as "low" | "mid" | "high";
  log.warn("config", `ignoring reflectBudget=${JSON.stringify(value)} — expected low|mid|high`);
  return "high";
}

/**
 * The page-refresh trigger a raw config asks for: the schedule kind, and the expression it fires
 * on.
 *
 * `"cron"` is the default and needs no `pageTriggerCron`: an omitted expression takes
 * DEFAULT_PAGE_TRIGGER_CRON, so the common config — none of these fields at all — is an hourly
 * staggered refresh. A user who wants pages to stop refreshing on their own writes "manual".
 *
 * A malformed `H` is refused here and not one step later: `expandCronHash` leaves an expression it
 * cannot read alone, so an unchecked `"H(9-3) * * * *"` would reach the server verbatim and fail
 * page creation with a cron parse error naming syntax this package invented. Such an expression
 * takes the default schedule, with a warning — the same as writing none. Ordinary cron syntax
 * stays unvalidated: the server owns that, and duplicating its parser here would only disagree
 * with it.
 */
function resolvePageTrigger(raw: RawConfig): {
  type: "auto-refresh" | "cron" | "manual";
  cron?: string;
} {
  if (raw.pageTriggerType === "manual") return { type: "manual" };
  if (raw.pageTriggerType === "auto-refresh") return { type: "auto-refresh" };
  if (raw.pageTriggerType !== undefined && raw.pageTriggerType !== "cron")
    log.warn(
      "config",
      `ignoring pageTriggerType=${JSON.stringify(raw.pageTriggerType)} — ` +
        "expected cron|auto-refresh|manual"
    );
  const cron = raw.pageTriggerCron?.trim();
  if (!cron) return { type: "cron", cron: DEFAULT_PAGE_TRIGGER_CRON };
  if (!isHashedCron(cron) || parseHashedCron(cron)) return { type: "cron", cron };
  log.warn(
    "config",
    `pageTriggerCron ${JSON.stringify(cron)} has a malformed hashed field — ` +
      'write `H` or `H(<lo>-<hi>)` within the field\'s own range, e.g. "H H(0-5) * * *" — ' +
      `falling back to ${JSON.stringify(DEFAULT_PAGE_TRIGGER_CRON)}`
  );
  return { type: "cron", cron: DEFAULT_PAGE_TRIGGER_CRON };
}

/**
 * Validate `pages`, dropping anything unusable with a warning.
 *
 * A key matching no seeded page is the mistake worth catching loudly: it reads as having disabled
 * or reworded something and silently does nothing, so the warning names it alongside the set that
 * would have worked. Values are checked for the same reason — `{"source_query": 5}` would
 * otherwise travel and become a page whose description is `5`.
 */
function resolvePages(raw: RawConfig["pages"]): PagesConfig {
  // Widened deliberately: this arrives from a hand-edited JSON file, so the declared type says
  // what is meant, not what is there.
  const value: unknown = raw;
  if (!value || typeof value !== "object" || Array.isArray(value)) return {};
  const out: PagesConfig = {};
  for (const [name, entry] of Object.entries(value as Record<string, unknown>)) {
    if (!PAGE_NAMES.some((known) => known.toLowerCase() === name.trim().toLowerCase())) {
      log.warn(
        "config",
        `ignoring pages[${JSON.stringify(name)}] — no seeded page has that name; ` +
          `expected one of: ${PAGE_NAMES.join(", ")}`
      );
      continue;
    }
    if (entry === false) {
      out[name] = false;
      continue;
    }
    const query: unknown =
      entry && typeof entry === "object" && !Array.isArray(entry)
        ? (entry as { source_query?: unknown }).source_query
        : undefined;
    if (typeof query === "string" && query.trim()) {
      out[name] = { source_query: query };
      continue;
    }
    log.warn(
      "config",
      `ignoring pages[${JSON.stringify(name)}]=${JSON.stringify(entry)} — ` +
        'expected false, or {"source_query": "<your question>"}'
    );
  }
  return out;
}

/**
 * Validate `customPages` — the pages a user defines, as opposed to the taxonomy that `pages`
 * reworks.
 *
 * A name colliding with a seeded page is refused rather than merged: the two keys mean different
 * things (rework the plugin's page vs. create your own), and picking one for the user would be a
 * guess. `tags` is optional and an absent one is not an empty page.
 */
function resolveCustomPages(raw: RawConfig["customPages"]): CustomPagesConfig {
  // Widened deliberately, like resolvePages: a hand-edited JSON file says what is meant, not what
  // is there.
  const value: unknown = raw;
  if (!value || typeof value !== "object" || Array.isArray(value)) return {};
  const out: CustomPagesConfig = {};
  for (const [rawName, entry] of Object.entries(value as Record<string, unknown>)) {
    const name = rawName.trim();
    if (!name) continue;
    if (PAGE_NAMES.some((known) => known.toLowerCase() === name.toLowerCase())) {
      log.warn(
        "config",
        `ignoring customPages[${JSON.stringify(rawName)}] — that is a seeded page; ` +
          "reword it under `pages` instead"
      );
      continue;
    }
    const query: unknown =
      entry && typeof entry === "object" && !Array.isArray(entry)
        ? (entry as { source_query?: unknown }).source_query
        : undefined;
    if (typeof query !== "string" || !query.trim()) {
      log.warn(
        "config",
        `ignoring customPages[${JSON.stringify(rawName)}]=${JSON.stringify(entry)} — ` +
          'expected {"source_query": "<your question>"}'
      );
      continue;
    }
    const rawTags: unknown = (entry as { tags?: unknown }).tags;
    // A stray number or nested object would reach the API as a tag and fail page creation.
    const tags = Array.isArray(rawTags)
      ? rawTags.filter((t): t is string => typeof t === "string" && t.trim() !== "")
      : [];
    out[name] = tags.length ? { source_query: query, tags } : { source_query: query };
  }
  return out;
}

/**
 * Validate `observationScopes`, falling back to the default on anything unrecognized.
 *
 * A typo here would otherwise reach the API as an unknown scoping mode and change how a whole
 * bank's observations are grouped, so an unusable value takes the default rather than travelling.
 * An empty list is unusable too, and specifically so: `[]` declares ZERO scopes, which the server
 * reads as "no spec" and silently treats as `combined` — the opposite of what writing this field
 * at all was meant to express.
 */
function resolveObservationScopes(raw: RawConfig["observationScopes"]): ObservationScopes {
  // Widened deliberately: this arrives from a hand-edited JSON file, so the declared type says what
  // is meant, not what is there.
  const value: unknown = raw;
  if (typeof value === "string")
    return (OBSERVATION_SCOPE_MODES as readonly string[]).includes(value)
      ? (value as ObservationScopes)
      : DEFAULT_OBSERVATION_SCOPES;
  if (Array.isArray(value)) {
    const scopes = (value as unknown[])
      .filter((scope): scope is unknown[] => Array.isArray(scope))
      .map((scope) => scope.filter((t): t is string => typeof t === "string" && t.trim() !== ""));
    if (scopes.length) return scopes;
  }
  return DEFAULT_OBSERVATION_SCOPES;
}

/**
 * Validate `defaultBankConfig`: an object, minus the fields the plugin governs itself.
 *
 * Same shape rule as `recallOptions` — an array would spread into numeric keys and reach the
 * import as garbage, so anything but a plain object contributes nothing. A governed key is dropped
 * here, with a warning naming the setting that owns it, rather than silently in the manifest: a
 * user who wrote `retain_strategies` there would otherwise watch it do nothing every session.
 */
function resolveDefaultBankConfig(raw: RawConfig["defaultBankConfig"]): Record<string, unknown> {
  const value: unknown = raw;
  if (!value || typeof value !== "object" || Array.isArray(value)) return {};
  const out: Record<string, unknown> = {};
  for (const [key, v] of Object.entries(value as Record<string, unknown>)) {
    if (PLUGIN_GOVERNED_BANK_FIELDS.includes(key)) {
      log.warn(
        "config",
        `ignoring defaultBankConfig.${key} — the plugin manages this field itself` +
          (key === "retain_extraction_mode" ? " (set retainExtractionMode instead)" : "")
      );
      continue;
    }
    out[key] = v;
  }
  return out;
}

/** Apply defaults to a raw (file) config. Pure — the single place the defaults live. */
export function resolveConfig(raw: RawConfig = {}): Config {
  const pageTrigger = resolvePageTrigger(raw);
  return {
    apiUrl: raw.apiUrl ?? DEFAULT_API_URL,
    apiToken: raw.apiToken || undefined,
    bankId: raw.bankId,
    dynamicBankId: raw.dynamicBankId,
    bankIdTemplate: raw.bankIdTemplate,
    mapPathToBank: raw.mapPathToBank,
    resolveWorktrees: raw.resolveWorktrees,
    optInOnly: raw.optInOnly ?? false,
    // Same shape as retainTags: a config typo must not become a path that silently approves nothing.
    optInPaths: Array.isArray(raw.optInPaths)
      ? raw.optInPaths.filter((p): p is string => typeof p === "string" && p.trim() !== "")
      : [],
    harness: raw.harness ?? "opencode",
    disabled: raw.disabled ?? false,
    retainSessions: raw.retainSessions ?? true, // write sessions back by default, every harness
    manageBankConfig: raw.manageBankConfig ?? true,
    retainExtractionMode: RETAIN_EXTRACTION_MODES.includes(raw.retainExtractionMode!)
      ? raw.retainExtractionMode!
      : DEFAULT_RETAIN_EXTRACTION_MODE,
    defaultBankConfig: resolveDefaultBankConfig(raw.defaultBankConfig),
    maxParallelRetains: raw.maxParallelRetains || 10,
    reflectTimeoutMs: raw.reflectTimeoutMs || DEFAULT_REFLECT_TIMEOUT_MS,
    // Inherit an explicitly-raised reflectTimeoutMs (that is what users reaching for a longer
    // reflect already set), but never let it LOWER the tool below the default — a short window is
    // set to bound the automatic hook, not to cut off a call the agent is waiting on.
    reflectToolTimeoutMs:
      raw.reflectToolTimeoutMs ||
      Math.max(raw.reflectTimeoutMs || 0, DEFAULT_REFLECT_TOOL_TIMEOUT_MS),
    injectTimeoutMs: raw.injectTimeoutMs || DEFAULT_INJECT_TIMEOUT_MS,
    reflectBudget: resolveReflectBudget(raw),
    autoInject: resolveAutoInject(raw),
    pageSearchLimit: raw.pageSearchLimit || DEFAULT_PAGE_SEARCH_LIMIT,
    // Same shape as retainMetadata: an object, or nothing. An array would spread into numeric
    // keys and reach the API as garbage, so it is rejected like any other non-object.
    recallOptions:
      raw.recallOptions && typeof raw.recallOptions === "object" && !Array.isArray(raw.recallOptions)
        ? { ...raw.recallOptions }
        : {},
    toolGuideExtra:
      typeof raw.toolGuideExtra === "string" && raw.toolGuideExtra.trim()
        ? raw.toolGuideExtra
        : undefined,
    // Every turn, not every tenth. The guide is what tells the agent WHEN to reach for memory, and
    // at a cadence of 10 a normal session is told once, on turn 1, and never again. The cost is
    // the guide's ~2KB re-sent per turn, which caches.
    pageRefreshEveryTurns: raw.pageRefreshEveryTurns || 1,
    pageTriggerType: pageTrigger.type,
    pageTriggerCron: pageTrigger.cron,
    pages: resolvePages(raw.pages),
    customPages: resolveCustomPages(raw.customPages),
    autoSeed: raw.autoSeed ?? true,
    seedLimit: raw.seedLimit || DEFAULT_SEED_LIMIT,
    codebaseSurvey: raw.codebaseSurvey ?? true,
    surveyModel: raw.surveyModel || "haiku",
    surveyBudgetUsd: raw.surveyBudgetUsd || 2,
    surveyRefreshCommits: raw.surveyRefreshCommits ?? 20,
    gitIngest: ["message", "full", "none"].includes(raw.gitIngest as string)
      ? (raw.gitIngest as "message" | "full" | "none")
      : "message",
    // Hostile input is a config typo, not an attack: keep only string entries so a stray number or
    // nested object cannot reach the API as a tag and fail the whole retain.
    retainTags: Array.isArray(raw.retainTags)
      ? raw.retainTags.filter((t): t is string => typeof t === "string" && t.trim() !== "")
      : [],
    // Undefined, not "", when unset or blank: the retain call distinguishes "not configured" (use
    // the built-in default) from a configured value, and an empty context would strip the default.
    retainContext:
      typeof raw.retainContext === "string" && raw.retainContext.trim() !== ""
        ? raw.retainContext
        : undefined,
    retainMetadata:
      raw.retainMetadata && typeof raw.retainMetadata === "object"
        ? Object.fromEntries(
            Object.entries(raw.retainMetadata).filter(([, v]) => typeof v === "string")
          )
        : {},
    observationScopes: resolveObservationScopes(raw.observationScopes),
    banks: raw.banks && typeof raw.banks === "object" ? raw.banks : {},
    paths: raw.paths && typeof raw.paths === "object" ? raw.paths : {},
    logLevel: ["debug", "info", "warn", "error"].includes(raw.logLevel as string)
      ? (raw.logLevel as "debug" | "info" | "warn" | "error")
      : "info",
    autoUpdate: raw.autoUpdate ?? true,
  };
}
