/**
 * The passive tier-tagging vocabulary.
 *
 * One hierarchical bank-config group, seeded at configure time: with "tag" on, the extractor copies
 * each selected "knowledge:<value>" onto the fact's tags, giving every durable fact a tier the
 * server-side knowledge base and any tag-filtered query can select on. The vocabulary is FIXED —
 * tag matching is exact set-ops, so a per-feature vocabulary would never match.
 */

export interface EntityLabelValue {
  value: string;
  description: string;
}

export interface EntityLabelGroup {
  key: string;
  type: "multi-values";
  optional: boolean;
  tag: boolean;
  description: string;
  values: EntityLabelValue[];
}

/**
 * The knowledge tiers. Most facts deserve NO label: routine, transient and operational work is
 * explicitly excluded, because a page built from noise is worse than no page.
 */
export const KNOWLEDGE_LABELS: EntityLabelGroup = {
  key: "knowledge",
  type: "multi-values", // 0, 1, or several — empty is normal
  optional: true,
  tag: true, // emits knowledge:<value> onto the fact's tags
  description:
    "Routing labels for this project's WenYuan KNOWLEDGE PAGES — curated, human-readable summaries " +
    "of the repo's DURABLE engineering knowledge (architecture, key decisions, conventions, ongoing " +
    "initiatives), each page rebuilt automatically from the facts labeled for it. Mark a fact only when " +
    "it is durable, reusable knowledge a developer would still want surfaced in future sessions. " +
    "IMPORTANT: leave this EMPTY for routine, transient, or operational facts — a passing test, a " +
    "one-off command, a status update, a debugging dead-end. MOST facts should get no label here. " +
    "Assign more than one value only when the fact genuinely fits several.",
  values: [
    {
      value: "feature-work",
      description:
        "A new feature, initiative, or enhancement being planned or built — the capability being added " +
        "and the intent behind it. Not routine bug-fixes or chores.",
    },
    {
      value: "decision",
      description:
        "A technical decision that will constrain future work, with its rationale — why this approach " +
        "was chosen over alternatives, or a rule deliberately adopted.",
    },
    {
      value: "convention",
      description:
        "An established way this project does things — naming, structure, testing, error handling, or " +
        "another recurring pattern a contributor is expected to follow.",
    },
    {
      value: "component",
      description:
        "What a specific module, file, service, or subsystem is responsible for, or how components " +
        "depend on and connect to one another.",
    },
    {
      value: "concept",
      description:
        "A domain concept, key abstraction, or piece of project vocabulary a new contributor must " +
        "understand to work effectively.",
    },
  ],
};
