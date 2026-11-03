/**
 * Per-bank and per-path overrides — the step that runs AFTER bank resolution, giving per-repo
 * opt-in/out (disable, retainSessions, gitIngest, survey settings, ...) from the ONE global
 * config file.
 */
import { isOptedIn, pathSection } from "../bank";
import type { Config, RawConfig } from "./raw";
import { resolveConfig } from "./resolve";

/** Bank-resolution fields are meaningless inside a `banks.<id>` section (they can't change the id
 *  that selected it) — strip them so a typo there can't silently re-route memory. */
const BANK_OVERRIDE_EXCLUDED = [
  "bankId",
  "bankIdTemplate",
  "mapPathToBank",
  "dynamicBankId",
  "resolveWorktrees",
  // Approval is decided BEFORE the bank is resolved, so a `banks.<id>` section naming these could
  // only ever arrive too late to matter — strip them rather than let them read as effective.
  "optInOnly",
  "optInPaths",
  "harness",
] as const;

/** An override section minus what it may not set: the resolution/approval fields and the nested
 *  maps (a section cannot carry sections of its own). */
function overrideFields(section: object): Record<string, unknown> {
  const safe: Record<string, unknown> = { ...section };
  for (const k of BANK_OVERRIDE_EXCLUDED) delete safe[k];
  delete safe.banks;
  delete safe.paths;
  return safe;
}

/** Resolve just the fields present in `patch` so an override only changes what it names. */
function resolvePartial(patch: RawConfig): Partial<Config> {
  const full = resolveConfig(patch);
  const out: Partial<Config> = {};
  for (const key of Object.keys(patch) as (keyof RawConfig)[]) {
    if (key in full)
      (out as Record<string, unknown>)[key] = (full as unknown as Record<string, unknown>)[key];
  }
  // The legacy key resolves into a differently-named field, so the loop above can't carry it.
  if ("autoReflect" in patch && !("autoInject" in patch)) out.autoInject = full.autoInject;
  return out;
}

/**
 * Apply the `banks.<bankId>` override section (if any) to an already-resolved config.
 *
 * Supplying `directory` enforces `optInOnly` and applies the `paths.<prefix>` section for that
 * directory — every entry point already has the directory to hand, having just passed it to bank
 * resolution.
 */
export function applyBankConfig(
  cfg: Config,
  resolvedId: string,
  directory?: string
): { cfg: Config; bankId: string } {
  // Rides the `disabled` gate every entry point already checks after bank resolution, rather than
  // adding a second thing eight call sites must remember — and that gate is known to stop the run
  // before anything creates a bank.
  if (directory !== undefined && !isOptedIn(cfg, directory))
    return { cfg: { ...cfg, disabled: true }, bankId: resolvedId };
  const byPath = directory === undefined ? undefined : pathSection(cfg, cfg.paths, directory);
  if (byPath) {
    // A path entry never renames the bank: only `banks.<id>` may, below.
    const { bank: _rename, ...rest } = overrideFields(byPath);
    cfg = { ...cfg, ...resolvePartial(rest as RawConfig) };
  }
  const section = cfg.banks[resolvedId];
  if (!section) return { cfg, bankId: resolvedId };
  const safe = overrideFields(section);
  // `bank` renames the destination — single hop, selected by the ORIGINAL resolved id (the
  // target's own banks section, if any, is deliberately NOT consulted: no chaining).
  const bankId = typeof safe.bank === "string" && safe.bank ? (safe.bank as string) : resolvedId;
  delete safe.bank;
  return { cfg: { ...cfg, ...resolvePartial(safe as RawConfig) }, bankId };
}
