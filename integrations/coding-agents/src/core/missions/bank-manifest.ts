/**
 * The bank template as one importable manifest, and the rule for applying it.
 *
 * The manifest carries the bank's CONFIG — missions, retain strategies, entity labels — for
 * POST /banks/{id}/import. It is idempotent (config fields apply as per-bank overrides), so the
 * deepen engine can apply it every run. It deliberately does NOT carry the seeded pages: the
 * template's mental-model key would create bare models with no knowledge-base node, invisible to
 * page search — pages are seeded through the knowledge-base API (client/knowledge.ts).
 */
import type { EntityLabelGroup } from "./knowledge-labels";
import { KNOWLEDGE_LABELS } from "./knowledge-labels";
import {
  DEFAULT_RETAIN_EXTRACTION_MODE,
  GIT_MISSION,
  OBSERVATIONS_MISSION,
  REFLECT_MISSION,
  RETAIN_STRATEGIES,
  type RetainExtractionMode,
} from "./retain-missions";

export const CODING_BANK_TEMPLATE = {
  version: "1",
  bank: {
    reflect_mission: REFLECT_MISSION,
    enable_observations: true,
    observations_mission: OBSERVATIONS_MISSION,
    retain_mission: GIT_MISSION,
    retain_extraction_mode: DEFAULT_RETAIN_EXTRACTION_MODE,
    retain_default_strategy: "git",
    retain_strategies: RETAIN_STRATEGIES,
    entity_labels: [KNOWLEDGE_LABELS],
    entities_allow_free_form: true,
  },
} as const;

/** Bank-level missions the template seeds ONCE and then leaves alone. */
const MISSION_FIELDS = ["reflect_mission", "retain_mission", "observations_mission"] as const;

/** Bank-scoped config OVERRIDES exactly as GET /banks/{id}/config reports them. */
export type BankOverrides = Record<string, unknown>;

/** The manifest configureBank POSTs to /banks/{id}/import. */
export interface BankManifest {
  version: "1";
  bank: Record<string, unknown>;
}

/**
 * Bank-config fields defaultBankConfig may NOT set, because this package governs them itself: the
 * two containers are merged per entry (see codingBankManifest) and the extraction mode follows the
 * package's own setting. A default naming one of these would either replace a merged map wholesale
 * or fight the mode re-sync.
 */
export const PLUGIN_GOVERNED_BANK_FIELDS: readonly string[] = [
  "retain_strategies",
  "entity_labels",
  "retain_extraction_mode",
];

/** A bank-config override the bank's owner actually made. Blank is not a choice; false is. */
function isSet(v: unknown): boolean {
  if (v === null || v === undefined) return false;
  if (typeof v === "string") return v.trim() !== "";
  return true;
}

/**
 * The template fields still missing from a bank whose overrides are "overrides" — or undefined
 * when it already carries them all and there is nothing to write.
 *
 * This package only ever ADDS what is missing; it never overwrites what the bank already says.
 * Re-applying the whole template on every pass is how a plugin takes a bank over, and each fix
 * that only protected the field last noticed left the rest stamped back on every session start.
 * Reading the current overrides and writing only where the bank is silent covers the whole
 * surface at once, including whatever gets added to the template next.
 *
 * The two container fields merge PER ENTRY: the server stores each as ONE config value and an
 * import replaces it outright, so re-sending the strategies wholesale deleted any strategy the
 * user had defined and could leave retain_default_strategy naming a strategy that no longer
 * existed. Merging still lets a strategy ADDED by a newer release reach an existing bank — the
 * reason the re-apply exists — without touching the entries already there. The deliberate
 * consequence: a release that REWORDS an existing strategy or label does not reach a bank that
 * already has it; clearing that override takes the new wording.
 *
 * ONE exception, a config key rather than a default: the extraction mode of the package's own
 * strategies follows the configured mode and is re-synced on drift, the way the page seeder
 * re-syncs a page's query. It is what every session write-back costs, so it must be changeable
 * and must reach banks seeded before the change. The strategies' other fields, and any strategy
 * this package did not define, are still left alone.
 *
 * "defaults" are the user's OWN additions to the template — bank-config fields written under the
 * same silent-only rule. They exist because a bank this package creates is otherwise born with
 * the server's defaults for everything the template does not name (auto-consolidation on, a short
 * mental-model refresh floor), which are exactly the expensive settings. On a key the template
 * also names, the user's default wins as the more specific statement.
 */
export function codingBankManifest(
  overrides: BankOverrides | undefined,
  mode: RetainExtractionMode = DEFAULT_RETAIN_EXTRACTION_MODE,
  defaults: Record<string, unknown> = {}
): BankManifest | undefined {
  // Unreadable overrides — the bank does not exist yet, or the deployment has the bank-config API
  // switched off. Nothing can have been customised through an API that is not there, and this
  // same POST is what CREATES the bank, so {} seeds the lot.
  const current = overrides ?? {};
  const template = CODING_BANK_TEMPLATE.bank;
  const bank: Record<string, unknown> = {};

  // The missions are seeded as a GROUP: they are written together, so any one of them being
  // present means the bank has been seeded already, or its owner wrote their own.
  if (!MISSION_FIELDS.some((f) => isSet(current[f]))) {
    bank.reflect_mission = template.reflect_mission;
    bank.enable_observations = template.enable_observations;
    bank.observations_mission = template.observations_mission;
    bank.retain_mission = template.retain_mission;
    bank.retain_extraction_mode = mode;
  }

  if (!isSet(current.retain_default_strategy))
    bank.retain_default_strategy = template.retain_default_strategy;
  if (!isSet(current.entities_allow_free_form))
    bank.entities_allow_free_form = template.entities_allow_free_form;

  const strategies =
    current.retain_strategies && typeof current.retain_strategies === "object"
      ? (current.retain_strategies as Record<string, unknown>)
      : {};
  const updates: Record<string, unknown> = {};
  for (const [name, def] of Object.entries(template.retain_strategies)) {
    // "custom" (the survey) carries its own instructions and is not the configured mode's to change.
    const synced = def.retain_extraction_mode === "custom" ? {} : { retain_extraction_mode: mode };
    const have = strategies[name] as Record<string, unknown> | null | undefined;
    if (!have || typeof have !== "object") updates[name] = { ...def, ...synced };
    else if ("retain_extraction_mode" in synced && have.retain_extraction_mode !== mode)
      updates[name] = { ...have, ...synced };
  }
  // The whole map is one config value, so the UNION has to be sent — not just the changes.
  if (Object.keys(updates).length > 0) bank.retain_strategies = { ...strategies, ...updates };

  const labels = Array.isArray(current.entity_labels) ? current.entity_labels : [];
  const knowledgeGroup: EntityLabelGroup = template.entity_labels[0];
  if (!labels.some((g) => (g as { key?: unknown } | null)?.key === knowledgeGroup.key))
    bank.entity_labels = [...labels, knowledgeGroup];

  // After the template, so a default wins the keys both name. The governed fields are dropped at
  // config resolution (with a warning) and skipped again here, so a caller of this function alone
  // cannot replace a merged container either.
  for (const [key, value] of Object.entries(defaults)) {
    if (PLUGIN_GOVERNED_BANK_FIELDS.includes(key) || !isSet(value) || isSet(current[key])) continue;
    bank[key] = value;
  }

  return Object.keys(bank).length > 0 ? { version: "1", bank } : undefined;
}
