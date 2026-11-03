/**
 * The extraction missions and retain strategies a coding project's memory is built with.
 *
 * A mission is the instruction the server's extractor reads for one kind of source; a strategy
 * binds a mission to the chunking that source needs. Both are plain data: they reach the bank as
 * bank-config values (see bank-manifest.ts), so this module has no HTTP and no agent harness in it.
 */

/** What a git commit (message + full diff) must yield: the decision and its cause, verbatim. */
export const GIT_MISSION =
  "You are ingesting a single git commit: its message and its full diff. Extract the concrete " +
  "technical DECISION and the CAUSE/INVARIANT it encodes, bound to the specific code entities " +
  "(functions, methods, files) and behaviors it changes. Preserve exact identifiers, paths, and " +
  "literal values verbatim. Preserve the 'REF-ID: <token>' marker verbatim in every fact. Capture " +
  "both WHAT changed and WHY. Issue/PR references (#123, GH-123, PROJ-123) are load-bearing: keep " +
  "them VERBATIM in the fact text and emit each as an ENTITY, so a later question about that issue " +
  "or PR retrieves this decision.";

/** What an aggregated commit-MESSAGE log yields: initiatives and themes, never per-line detail. */
export const GITLOG_MISSION =
  "You are ingesting an aggregated block of git commit MESSAGES ONLY (no diffs) — the project's " +
  "recent commit-message history, oldest first. Extract the project's INITIATIVES, FEATURES, " +
  "ENHANCEMENTS, and notable changes or THEMES over time — what the project has been working on and " +
  "how it has evolved. Do NOT extract per-line code detail (there is no diff to draw it from). Group " +
  "related commits into a coherent initiative/theme where the messages make that clear; preserve exact " +
  "identifiers and literal values verbatim when quoting a subject line. Keep issue/PR references " +
  "(#123, GH-123) VERBATIM and emit each as an ENTITY — they are how future sessions will ask about " +
  "this work.";

/**
 * One mission for every developer conversation, backfilled or live. Its two hard rules keep a
 * transcript from becoming noise: final state wins (a conversation revises itself, so only the
 * LAST value of a setting is a fact), and fewest facts that capture the outcome — never one per
 * message or per tool turn.
 */
export const CONVERSATION_MISSION =
  "You are ingesting a developer conversation as a JSONL transcript (one {role, content} turn per line): the " +
  "user's requests, the assistant's narration, and compact 'action' turns naming each tool use and " +
  'its target (e.g. "Edit boltons/strutils.py") with no arguments or outputs. It may be a SHORT ' +
  "decision chat or a LONG working session — scale the facts to the substance, never to the message " +
  "count. Extract the FEWEST facts that capture the OUTCOME: the settled DECISIONS and their exact " +
  "rules/values (quote literals VERBATIM); concrete CHANGES to specific code entities; problems and " +
  "how they were resolved; conventions or invariants established; at most one fact for a notable " +
  "REJECTED alternative ('initially proposed X, changed to Y because Z'). A short decision chat " +
  "usually yields 1-2 facts; a substantial working session several. CRITICAL: a conversation REVISES " +
  "itself — record ONLY the FINAL state as what is in effect; a superseded proposal appears ONLY " +
  "inside the rejected fact, NEVER as its own 'decided' fact; if the same setting changes several " +
  "times keep only the LAST, and make unmistakably clear which choice WON. Do NOT emit one fact per " +
  "message, per intermediate proposal, or per action turn. Keep issue/PR references (#123, GH-123) " +
  "VERBATIM and emit each as an ENTITY. Preserve the 'REF-ID: <token>' marker " +
  "verbatim in every fact. Do not invent; capture only what was actually settled.";

/** Reflect's mission: root-cause recall where the latest decision wins and an honest miss beats a
 *  confident wrong answer. */
export const REFLECT_MISSION =
  "You are a debugging assistant with the project's past decisions in memory (git rationale and " +
  "developer chats). Given a bug's SYMPTOM, find the past decision whose rationale explains the ROOT " +
  "CAUSE — not one that merely shares vocabulary. Answer with the PRECISE fix: state the EXACT rule " +
  "and the LITERAL values, identifiers, strings, numbers, or set members that were decided — quote " +
  "them VERBATIM, never paraphrase, generalize, or omit them (give the actual decided value, not " +
  "'the project standard'). If memories CONFLICT on the same rule, the LATEST decision wins — " +
  "prefer facts that explicitly amend or supersede an earlier one, state the superseded rule as " +
  "no longer in effect, and never present it as the fix. Name the function/file to change and " +
  "cite the REF-ID(s). If NOTHING in memory genuinely explains THIS symptom, say exactly that in " +
  "one short sentence — a wrong-but-confident nearest match is worse than an honest miss; never " +
  "stretch an unrelated decision to fit.";

/** A standalone document (notes, docs, structural findings) keeps its facts and structure. */
export const DOCUMENT_MISSION =
  "You are ingesting a standalone document (notes, docs, or structural findings). Extract the " +
  "concrete facts, concepts, and structure it describes.";

/** Consolidation guidance: durable structure over one-off detail; revise in place, never fork. */
export const OBSERVATIONS_MISSION =
  "Consolidate durable knowledge about THIS codebase — recurring patterns, conventions, module " +
  "responsibilities, and how components relate — from the ingested commits and conversations. " +
  "Favor stable structural understanding over one-off details. When a new fact contradicts or " +
  "supersedes an existing observation, UPDATE that observation to reflect the current state rather " +
  "than creating a sibling alongside it; note that the rule was revised and when, so the superseded " +
  "version is visible as history rather than as a competing claim.";

/** Extraction modes a strategy can run under; "custom" needs instructions this package does not
 *  ship, so it is only ever read (the survey strategy), never offered as a choice. */
export const RETAIN_EXTRACTION_MODES = ["concise", "verbose", "verbatim", "chunks"] as const;
export type RetainExtractionMode = (typeof RETAIN_EXTRACTION_MODES)[number];

/**
 * "concise" is the default on purpose: every session write-back runs under it, so the mode is
 * paid per turn, and the verbose mode priced one active session out of affordability.
 */
export const DEFAULT_RETAIN_EXTRACTION_MODE: RetainExtractionMode = "concise";

/** A large chunk keeps an aggregated document, a conversation or a message log in ONE chunk, so
 *  the extractor sees the whole arc instead of per-chunk fragments; only long input still splits. */
const AGGREGATE_CHUNK_SIZE = 12_000;

/**
 * The strategies memories are written under; keys are the strategy names a retain call sends. A
 * bank that does not know a name falls back to its own config silently, which is why seeding them
 * (bank-manifest.ts) matters.
 */
export const RETAIN_STRATEGIES = {
  git: { retain_mission: GIT_MISSION, retain_extraction_mode: DEFAULT_RETAIN_EXTRACTION_MODE },
  // One aggregated messages-only document: few, big chunks so the extractor sees the whole arc.
  gitlog: {
    retain_mission: GITLOG_MISSION,
    retain_extraction_mode: DEFAULT_RETAIN_EXTRACTION_MODE,
    retain_chunk_size: AGGREGATE_CHUNK_SIZE,
  },
  // Backfilled decision chats and live working sessions are the same content in the same format;
  // the mission scales extraction to the substance and keeps only the final state. The large
  // chunk size holds a typical conversation whole — the default would split it mid-revision.
  conversation: {
    retain_mission: CONVERSATION_MISSION,
    retain_extraction_mode: DEFAULT_RETAIN_EXTRACTION_MODE,
    retain_chunk_size: AGGREGATE_CHUNK_SIZE,
  },
  // Documents are not dialogue: the conversation mission's rejected-alternative rule does not
  // apply, and documents run long enough to want the room.
  document: {
    retain_mission: DOCUMENT_MISSION,
    retain_extraction_mode: DEFAULT_RETAIN_EXTRACTION_MODE,
    retain_chunk_size: AGGREGATE_CHUNK_SIZE,
  },
  // Survey-lifecycle documents carry conditional rules: the lifecycle's internal status markers
  // must extract NOTHING, while real findings extract as concrete structural facts.
  survey: {
    retain_extraction_mode: "custom",
    retain_custom_instructions:
      "This document belongs to the WenYuan codebase-survey lifecycle. Apply ONE of two rules: " +
      "(1) If the content is an internal status marker — it says it is an internal marker, or " +
      "merely announces that a survey started/completed at some commit — extract NOTHING: return " +
      "an empty list of facts. (2) Otherwise the content is survey FINDINGS about the codebase: " +
      "extract the concrete structural facts it states (components and their responsibilities, " +
      "key concepts, conventions, tech stack), preserving identifiers verbatim.",
    retain_chunk_size: AGGREGATE_CHUNK_SIZE,
  },
} as const;
