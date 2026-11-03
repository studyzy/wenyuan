/**
 * The config LAYERS: environment fallback, the file itself, the asking harness's section.
 * Precedence lives here; the field defaults live in resolve.ts.
 */
import { readFileSync } from "node:fs";
import { storedConfigPath } from "../../config/paths";
import type { Config, LoadOptions, RawConfig } from "./raw";
import { resolveConfig } from "./resolve";

/**
 * THE config file. `WENYUAN_AGENTS_CONFIG` points at a different one — for containers and test
 * harnesses where the home directory is not the right anchor. Still one file; without the
 * override it is the installer's own store (src/config/paths.ts), so the connection keys the
 * installer writes are read as this config's apiUrl/apiToken/bankId.
 */
function configFilePath(): string {
  return process.env.WENYUAN_AGENTS_CONFIG || storedConfigPath();
}

function readRaw(path: string): RawConfig {
  try {
    return JSON.parse(readFileSync(path, "utf8")) as RawConfig;
  } catch (e) {
    if ((e as NodeJS.ErrnoException)?.code !== "ENOENT") {
      console.error(`wenyuan-agents: ignoring invalid config at ${path}: ${(e as Error)?.message || e}`);
    }
    return {};
  }
}

/** Shallow-merge b over a; `harnesses` never survives into a layer; `banks` merges by bank id and
 *  `paths` by prefix. */
function mergeRaw(a: RawConfig, b: RawConfig): RawConfig {
  const { harnesses: _drop, ...flat } = b;
  return {
    ...a,
    ...flat,
    banks: { ...(a.banks ?? {}), ...(b.banks ?? {}) },
    paths: { ...(a.paths ?? {}), ...(b.paths ?? {}) },
  };
}

/** Apply one config layer (its top level, then its `harnesses.<name>` override) over `raw`. */
function applyLayer(raw: RawConfig, layer: RawConfig, harness?: string): RawConfig {
  let out = mergeRaw(raw, layer);
  const perHarness = harness ? layer.harnesses?.[harness] : undefined;
  if (perHarness) out = mergeRaw(out, perHarness);
  return out;
}

/**
 * Env var backing each scalar setting: `WENYUAN_AGENTS_` + the field in SCREAMING_SNAKE.
 *
 * These are a FALLBACK, not an override — the config file still wins wherever it sets a value, so
 * an existing file behaves exactly as before. They exist for the places a file is awkward:
 * containers, CI, and secret managers that inject a token rather than writing a credential to
 * disk.
 *
 * The map-valued settings (mapPathToBank, harnesses, banks, retainMetadata, recallOptions, pages,
 * customPages, defaultBankConfig) are deliberately absent: they are structures whose whole point
 * is per-repo/per-harness/per-key branching, which does not survive flattening into one env var.
 * They stay file-only.
 */
const ENV_KEYS = {
  apiUrl: "WENYUAN_AGENTS_API_URL",
  apiToken: "WENYUAN_AGENTS_API_TOKEN",
  bankId: "WENYUAN_AGENTS_BANK_ID",
  dynamicBankId: "WENYUAN_AGENTS_DYNAMIC_BANK_ID",
  bankIdTemplate: "WENYUAN_AGENTS_BANK_ID_TEMPLATE",
  resolveWorktrees: "WENYUAN_AGENTS_RESOLVE_WORKTREES",
  optInOnly: "WENYUAN_AGENTS_OPT_IN_ONLY",
  optInPaths: "WENYUAN_AGENTS_OPT_IN_PATHS",
  harness: "WENYUAN_AGENTS_HARNESS",
  disabled: "WENYUAN_AGENTS_DISABLED",
  retainSessions: "WENYUAN_AGENTS_RETAIN_SESSIONS",
  maxParallelRetains: "WENYUAN_AGENTS_MAX_PARALLEL_RETAINS",
  reflectTimeoutMs: "WENYUAN_AGENTS_REFLECT_TIMEOUT_MS",
  reflectToolTimeoutMs: "WENYUAN_AGENTS_REFLECT_TOOL_TIMEOUT_MS",
  injectTimeoutMs: "WENYUAN_AGENTS_INJECT_TIMEOUT_MS",
  reflectBudget: "WENYUAN_AGENTS_REFLECT_BUDGET",
  autoInject: "WENYUAN_AGENTS_AUTO_INJECT",
  pageSearchLimit: "WENYUAN_AGENTS_PAGE_SEARCH_LIMIT",
  toolGuideExtra: "WENYUAN_AGENTS_TOOL_GUIDE_EXTRA",
  autoReflect: "WENYUAN_AGENTS_AUTO_REFLECT",
  pageRefreshEveryTurns: "WENYUAN_AGENTS_PAGE_REFRESH_EVERY_TURNS",
  pageTriggerType: "WENYUAN_AGENTS_PAGE_TRIGGER_TYPE",
  pageTriggerCron: "WENYUAN_AGENTS_PAGE_TRIGGER_CRON",
  autoSeed: "WENYUAN_AGENTS_AUTO_SEED",
  seedLimit: "WENYUAN_AGENTS_SEED_LIMIT",
  codebaseSurvey: "WENYUAN_AGENTS_CODEBASE_SURVEY",
  surveyModel: "WENYUAN_AGENTS_SURVEY_MODEL",
  surveyBudgetUsd: "WENYUAN_AGENTS_SURVEY_BUDGET_USD",
  surveyRefreshCommits: "WENYUAN_AGENTS_SURVEY_REFRESH_COMMITS",
  logLevel: "WENYUAN_AGENTS_LOG_LEVEL",
  autoUpdate: "WENYUAN_AGENTS_AUTO_UPDATE",
  gitIngest: "WENYUAN_AGENTS_GIT_INGEST",
  // Scalar modes only ("shared", "combined", "per_tag", "all_combinations"). An explicit scope
  // list is a list OF lists, which does not survive flattening into one variable — file-only.
  observationScopes: "WENYUAN_AGENTS_OBSERVATION_SCOPES",
  // Comma-separated, e.g. WENYUAN_AGENTS_RETAIN_TAGS="project:{gitProject},env:work". A LIST rather
  // than a map, so it flattens cleanly; its sibling retainMetadata stays file-only for the reason
  // above.
  retainTags: "WENYUAN_AGENTS_RETAIN_TAGS",
  manageBankConfig: "WENYUAN_AGENTS_MANAGE_BANK_CONFIG",
  retainExtractionMode: "WENYUAN_AGENTS_RETAIN_EXTRACTION_MODE",
} as const satisfies Partial<Record<keyof RawConfig, string>>;

/** Fields parsed as booleans/numbers/comma-separated lists; everything else is taken as a string. */
const ENV_BOOLEANS = new Set<keyof RawConfig>([
  "dynamicBankId",
  "resolveWorktrees",
  "optInOnly",
  "disabled",
  "retainSessions",
  "autoReflect",
  "autoSeed",
  "codebaseSurvey",
  "autoUpdate",
  "manageBankConfig",
]);
const ENV_LISTS = new Set<keyof RawConfig>(["retainTags", "optInPaths"]);
const ENV_NUMBERS = new Set<keyof RawConfig>([
  "maxParallelRetains",
  "reflectTimeoutMs",
  "reflectToolTimeoutMs",
  "injectTimeoutMs",
  "pageSearchLimit",
  "pageRefreshEveryTurns",
  "seedLimit",
  "surveyBudgetUsd",
  "surveyRefreshCommits",
]);

/**
 * Read the env fallback layer. An unset or empty var contributes nothing (so it can't mask a file
 * value), and a malformed number is ignored with a warning rather than silently becoming NaN —
 * which would otherwise disable a timeout or cadence in a way that is very hard to trace.
 */
export function readEnvConfig(env: NodeJS.ProcessEnv = process.env): RawConfig {
  const out: Record<string, unknown> = {};
  for (const [key, name] of Object.entries(ENV_KEYS) as [keyof RawConfig, string][]) {
    const value = env[name];
    if (value === undefined || value.trim() === "") continue;
    if (ENV_BOOLEANS.has(key)) {
      out[key] = ["1", "true", "yes", "on"].includes(value.trim().toLowerCase());
    } else if (ENV_LISTS.has(key)) {
      // Empty entries dropped, so a trailing comma or "a,,b" is a typo rather than an empty tag.
      const items = value
        .split(",")
        .map((v) => v.trim())
        .filter(Boolean);
      if (items.length) out[key] = items;
    } else if (ENV_NUMBERS.has(key)) {
      const n = Number(value);
      if (Number.isFinite(n)) out[key] = n;
      else console.error(`wenyuan-agents: ignoring ${name}=${value} — not a number`);
    } else {
      out[key] = value;
    }
  }
  return out as RawConfig;
}

/** Load + resolve config from THE config file, with env as a fallback. Missing file -> defaults. */
export function loadConfig(opts: LoadOptions | string = {}): Config {
  const o: LoadOptions = typeof opts === "string" ? { path: opts } : opts; // legacy: loadConfig(path)
  // Env first so the FILE wins on any field it sets.
  const withEnv = applyLayer({}, readEnvConfig(), o.harness);
  const raw = applyLayer(withEnv, readRaw(o.path ?? configFilePath()), o.harness);
  // The harness that ASKED is the correct fallback for an unset `harness` field — not a hardcoded
  // default, whose silent misfiled every other harness's background seed into one agent's bank.
  // An explicit `harness:` in the config file still wins.
  if (!raw.harness && o.harness) raw.harness = o.harness;
  return resolveConfig(raw);
}
