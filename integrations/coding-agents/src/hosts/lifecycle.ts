import { OWNERSHIP_MARKER } from "../write/marker";
import { HOOKS_CONTAINER_KEY } from "./hook-shape";
import {
  LIFECYCLE_STAGES,
  type AdditionalRegistration,
  type BudgetUnit,
  type HostLifecycle,
  type LifecycleRegistration,
  type LifecycleStage,
  type RegistrationStyle,
} from "./lifecycle-types";

/**
 * The per-host lifecycle declaration: the one place the three lifecycle
 * registrations are described for every approved host.
 *
 * It is the single source the installer and the packaged entry points both
 * resolve through, so a host cannot be installed with one lifecycle and
 * executed with another. The three stage names, the three packaged script
 * files and the per-host event spellings, budgets and shape style all live
 * here and nowhere else.
 *
 * The events are the hosts' own lifecycle vocabulary and are contracts: they
 * are written into each host's config verbatim and must not be renamed. Every
 * host that declares a lifecycle here reads one of two registration shapes, so
 * the per-host variation is the shape style, the config file location (owned by
 * the host adapters), the script file name and the numeric budget.
 */

/** The three packaged hook script files, named once. */
export const HOOK_SCRIPT_FILES: Readonly<Record<LifecycleStage, string>> = {
  "session-start": "session-start.mjs",
  "pre-prompt": "pre-prompt.mjs",
  stop: "stop.mjs",
};

/**
 * The budgets every approved host declares, in seconds.
 *
 * The two interactive stages are bounded tightly because they run while the
 * user waits. The stop stage is allowed strictly more, because it writes a
 * whole conversation back and self-imposes a deadline smaller than the budget
 * the host enforces.
 */
const INTERACTIVE_BUDGET_SECONDS = 30;
const STOP_BUDGET_SECONDS = 60;

/** The fixed positive margin the stop hook keeps inside its budget, in seconds. */
export const STOP_DEADLINE_MARGIN_SECONDS = 5;

/** One registration, with the script file resolved from the shared table. */
function registration(
  stage: LifecycleStage,
  event: string,
  budget: number,
  budgetUnit: BudgetUnit,
  style: RegistrationStyle,
): LifecycleRegistration {
  return { event, scriptFile: HOOK_SCRIPT_FILES[stage], budget, budgetUnit, style };
}

/**
 * The key path from the config document root to the map of lifecycle events,
 * per host. Most hosts read a `hooks` member; Factory Droid reads the event
 * map at the document root itself; Antigravity keeps each integration's events
 * in its own namespace key so it cannot collide with other global hook
 * bundles; ZCode nests its event map one level deeper, under `hooks.events`.
 * A host absent here reads the standard `hooks` container.
 */
const HOOK_CONTAINER_PATHS: Readonly<Record<string, readonly string[]>> = {
  "antigravity-cli": [OWNERSHIP_MARKER],
  "factory-droid": [],
  zcode: ["hooks", "events"],
};

/** The key path the host's lifecycle events live under, from the document root. */
export function hookContainerPath(hostId: string): readonly string[] {
  return HOOK_CONTAINER_PATHS[hostId] ?? [HOOKS_CONTAINER_KEY];
}

/**
 * The key path the hook events are merged into, with the event key appended:
 * the path every event write and removal resolves through.
 */
export function hookEventPath(hostId: string, event: string): string[] {
  return [...hookContainerPath(hostId), event];
}

/** One member install seeds beside the events, and how firmly it is written. */
export interface ContainerSeed {
  /** The key path from the document root to the member, event container included. */
  path: readonly string[];
  /** The member name under that path. */
  member: string;
  /** The value to write. */
  value: unknown;
  /**
   * `set` overwrites whatever is there, because the host ships the member with
   * a default that keeps the hook runner off; `default` fills it only when
   * absent, because a user who tuned it owns the value.
   */
  mode: "set" | "default";
}

/**
 * The members install seeds beside the lifecycle events, per host. Only hosts
 * whose hook config is plain JSON declare seeds: the JSON document merge writes
 * them, and a host edited as text would need a second text edit per member.
 * ZCode's hook runner ships disabled and its output cap drops oversized
 * injections whole, so install turns the runner on and fills the cap; TraeCode
 * writes and expects a document version beside its hooks.
 */
const HOOK_CONTAINER_SEEDS: Readonly<Record<string, readonly ContainerSeed[]>> = {
  zcode: [
    { path: ["hooks"], member: "enabled", value: true, mode: "set" },
    { path: ["hooks"], member: "maxOutputBytes", value: 32768, mode: "default" },
  ],
  traecode: [{ path: [], member: "version", value: 1, mode: "default" }],
};

/** The members install seeds beside the events for one host. */
export function hookContainerSeeds(hostId: string): readonly ContainerSeed[] {
  return HOOK_CONTAINER_SEEDS[hostId] ?? [];
}

/**
 * The native events beyond the three shared stages that a host registers, each
 * reusing a shared stage's packaged script and budget. Grok Build delivers
 * recalled context beside tool results because its prompt hook can only block;
 * Factory Droid announces a user cancellation with a Notification instead of
 * Stop, and the stop script's idempotent write-back captures that final turn.
 */
const HOST_ADDITIONAL_REGISTRATIONS: Readonly<Record<string, readonly AdditionalRegistration[]>> = {
  "grok-build": [{ event: "PostToolUse", stage: "pre-prompt", style: "matcher-group" }],
  "factory-droid": [{ event: "Notification", stage: "stop", style: "matcher-group" }],
};

/** The additional native registrations one host declares. */
export function additionalRegistrations(hostId: string): readonly AdditionalRegistration[] {
  return HOST_ADDITIONAL_REGISTRATIONS[hostId] ?? [];
}

/**
 * The lifecycle every approved host declares, keyed by the shared stage names.
 * Hosts that differ later are expressed by a different entry here, never by a
 * conditional in a consumer.
 */
export const HOST_LIFECYCLES: Readonly<Record<string, HostLifecycle>> = {
  "claude-code": {
    "session-start": registration("session-start", "SessionStart", INTERACTIVE_BUDGET_SECONDS, "seconds", "matcher-group"),
    "pre-prompt": registration("pre-prompt", "UserPromptSubmit", INTERACTIVE_BUDGET_SECONDS, "seconds", "matcher-group"),
    stop: registration("stop", "Stop", STOP_BUDGET_SECONDS, "seconds", "matcher-group"),
  },
  codex: {
    "session-start": registration("session-start", "SessionStart", INTERACTIVE_BUDGET_SECONDS, "seconds", "matcher-group"),
    "pre-prompt": registration("pre-prompt", "UserPromptSubmit", INTERACTIVE_BUDGET_SECONDS, "seconds", "matcher-group"),
    stop: registration("stop", "Stop", STOP_BUDGET_SECONDS, "seconds", "matcher-group"),
  },
  codebuddy: {
    "session-start": registration("session-start", "SessionStart", INTERACTIVE_BUDGET_SECONDS, "seconds", "matcher-group"),
    "pre-prompt": registration("pre-prompt", "UserPromptSubmit", INTERACTIVE_BUDGET_SECONDS, "seconds", "matcher-group"),
    stop: registration("stop", "Stop", STOP_BUDGET_SECONDS, "seconds", "matcher-group"),
  },
  // WorkBuddy reads the same hook registration shape CodeBuddy does, with the
  // PascalCase event names and a seconds budget, so its lifecycle is declared
  // identically and only the file it is written into differs.
  workbuddy: {
    "session-start": registration("session-start", "SessionStart", INTERACTIVE_BUDGET_SECONDS, "seconds", "matcher-group"),
    "pre-prompt": registration("pre-prompt", "UserPromptSubmit", INTERACTIVE_BUDGET_SECONDS, "seconds", "matcher-group"),
    stop: registration("stop", "Stop", STOP_BUDGET_SECONDS, "seconds", "matcher-group"),
  },
  // Cursor CLI and Copilot CLI keep one object per event rather than a list of
  // matcher groups, and spell the event names in lower camel case.
  "cursor-cli": {
    "session-start": registration("session-start", "sessionStart", INTERACTIVE_BUDGET_SECONDS, "seconds", "flat-entry"),
    "pre-prompt": registration("pre-prompt", "beforeSubmitPrompt", INTERACTIVE_BUDGET_SECONDS, "seconds", "flat-entry"),
    stop: registration("stop", "stop", STOP_BUDGET_SECONDS, "seconds", "flat-entry"),
  },
  "copilot-cli": {
    "session-start": registration("session-start", "sessionStart", INTERACTIVE_BUDGET_SECONDS, "seconds", "flat-entry"),
    "pre-prompt": registration("pre-prompt", "userPromptTransformed", INTERACTIVE_BUDGET_SECONDS, "seconds", "flat-entry"),
    stop: registration("stop", "agentStop", STOP_BUDGET_SECONDS, "seconds", "flat-entry"),
  },

  // Antigravity has no SessionStart event: its single PreInvocation is the only
  // pre-model point that can inject context. Both interactive stages declare it,
  // and the dedup in orderedRegistrations keeps the last one — the pre-prompt
  // registration, whose inject-once guard covers the first-invocation moment.
  // A session-start registration on the same event would run its recall query
  // on every invocation, which the reference's one-entry rule also refuses.
  "antigravity-cli": {
    "session-start": registration("session-start", "PreInvocation", INTERACTIVE_BUDGET_SECONDS, "seconds", "plain-entry"),
    "pre-prompt": registration("pre-prompt", "PreInvocation", INTERACTIVE_BUDGET_SECONDS, "seconds", "plain-entry"),
    stop: registration("stop", "Stop", STOP_BUDGET_SECONDS, "seconds", "plain-entry"),
  },
  // Devin speaks Claude Code's hook protocol and matcher-group shape verbatim;
  // only the config file, the harness variable and the transcript story differ.
  "devin-cli": {
    "session-start": registration("session-start", "SessionStart", INTERACTIVE_BUDGET_SECONDS, "seconds", "matcher-group"),
    "pre-prompt": registration("pre-prompt", "UserPromptSubmit", INTERACTIVE_BUDGET_SECONDS, "seconds", "matcher-group"),
    stop: registration("stop", "Stop", STOP_BUDGET_SECONDS, "seconds", "matcher-group"),
  },
  // Grok Build speaks Claude's shape but delivers model-facing context from TOOL
  // hooks only: SessionStart is observe-only and UserPromptSubmit can only
  // block. The additional PostToolUse registration is what actually delivers
  // the recalled context beside the next tool result.
  "grok-build": {
    "session-start": registration("session-start", "SessionStart", INTERACTIVE_BUDGET_SECONDS, "seconds", "matcher-group"),
    "pre-prompt": registration("pre-prompt", "UserPromptSubmit", INTERACTIVE_BUDGET_SECONDS, "seconds", "matcher-group"),
    stop: registration("stop", "Stop", STOP_BUDGET_SECONDS, "seconds", "matcher-group"),
  },
  // Qwen Code's registration shape is Claude Code's; only the budget unit is
  // different, and it is a trap: Qwen passes the value straight to setTimeout,
  // so a seconds spelling there registers hooks that die before a Node process
  // starts. The budgets below are milliseconds for this host alone.
  "qwen-code": {
    "session-start": registration("session-start", "SessionStart", 30_000, "milliseconds", "matcher-group"),
    "pre-prompt": registration("pre-prompt", "UserPromptSubmit", 30_000, "milliseconds", "matcher-group"),
    stop: registration("stop", "Stop", 60_000, "milliseconds", "matcher-group"),
  },
  // Factory Droid speaks Claude Code's protocol field for field, and reads its
  // user-level hooks.json as the EVENT MAP itself — no wrapping container. It
  // also emits a Notification (not Stop) after a user cancellation, so the
  // additional registration reuses the stop script's idempotent write-back.
  "factory-droid": {
    "session-start": registration("session-start", "SessionStart", INTERACTIVE_BUDGET_SECONDS, "seconds", "matcher-group"),
    "pre-prompt": registration("pre-prompt", "UserPromptSubmit", INTERACTIVE_BUDGET_SECONDS, "seconds", "matcher-group"),
    stop: registration("stop", "Stop", STOP_BUDGET_SECONDS, "seconds", "matcher-group"),
  },
  // ZCode embeds the Claude Code agent runtime and speaks its hook protocol,
  // but spawns hooks without a shell and reads its budget in milliseconds, so
  // the registration is the process-argv shape under hooks.events.
  zcode: {
    "session-start": registration("session-start", "SessionStart", 30_000, "milliseconds", "process-entry"),
    "pre-prompt": registration("pre-prompt", "UserPromptSubmit", 30_000, "milliseconds", "process-entry"),
    stop: registration("stop", "Stop", 60_000, "milliseconds", "process-entry"),
  },
  // TraeCode speaks Claude Code's protocol with the registrations nested under
  // the top-level hooks key of its own hooks.json (unlike Factory Droid, whose
  // event map IS the file).
  traecode: {
    "session-start": registration("session-start", "SessionStart", INTERACTIVE_BUDGET_SECONDS, "seconds", "matcher-group"),
    "pre-prompt": registration("pre-prompt", "UserPromptSubmit", INTERACTIVE_BUDGET_SECONDS, "seconds", "matcher-group"),
    stop: registration("stop", "Stop", STOP_BUDGET_SECONDS, "seconds", "matcher-group"),
  },
  // Kimi Code takes a flat array of hook tables in its config.toml, validated
  // against a strict key set; the entries carry the event, the command and a
  // seconds timeout and nothing else. Its config is written as a marked TOML
  // block rather than through the JSON machinery.
  kimi: {
    "session-start": registration("session-start", "SessionStart", INTERACTIVE_BUDGET_SECONDS, "seconds", "table-array"),
    "pre-prompt": registration("pre-prompt", "UserPromptSubmit", INTERACTIVE_BUDGET_SECONDS, "seconds", "table-array"),
    stop: registration("stop", "Stop", STOP_BUDGET_SECONDS, "seconds", "table-array"),
  },
  // DSH declares NO lifecycle: its wiring is the Cordis plugin this package
  // ships (src/plugins/dsh.ts), which attaches memory on the harness's own
  // extension points in-process. Installing Claude-Code-style command hooks
  // beside it would recall twice per prompt and retain the transcript twice.
};

/**
 * The lifecycle for one host, in the shared stage order. A host that is not
 * declared is a programming error: every selectable host is installable, and
 * the registry is the only place a host is named.
 */
export function hostLifecycle(hostId: string): HostLifecycle {
  const lifecycle: HostLifecycle | undefined = HOST_LIFECYCLES[hostId];
  if (!lifecycle) {
    throw new Error(`no lifecycle is declared for host "${hostId}"`);
  }
  return lifecycle;
}

/** The registered hosts that carry a lifecycle declaration. */
export function declaredHostIds(): string[] {
  return Object.keys(HOST_LIFECYCLES);
}

/**
 * The registration one host declares for one stage. A missing host or stage is
 * a programming error: install and the packaged entry points both resolve
 * through this, so a mismatch cannot ship silently.
 */
export function registrationFor(hostId: string, stage: LifecycleStage): LifecycleRegistration {
  const registration: LifecycleRegistration | undefined = hostLifecycle(hostId)[stage];
  if (registration === undefined) {
    throw new Error(`host "${hostId}" declares no "${stage}" stage`);
  }
  return registration;
}

/**
 * The lifecycle for one host as an ordered list of stages, so a consumer
 * iterates in the declared order rather than the object's key order.
 */
export function orderedRegistrations(
  lifecycle: HostLifecycle,
): { stage: LifecycleStage; registration: LifecycleRegistration }[] {
  const declared = LIFECYCLE_STAGES.map((stage) => ({ stage, registration: lifecycle[stage] }))
    .filter((entry): entry is { stage: LifecycleStage; registration: LifecycleRegistration } =>
      entry.registration !== undefined,
    );
  // Two stages that declare the same native event register one entry, not two:
  // the host runs one hook process per event, and the last stage to declare it
  // wins because its script is the one whose stage behavior matches the firing
  // moment (see the antigravity-cli note above). The surviving entries keep the
  // shared stage order.
  const lastForEvent = new Map<string, { stage: LifecycleStage; registration: LifecycleRegistration }>();
  for (const entry of declared) {
    lastForEvent.set(entry.registration.event, entry);
  }
  return declared.filter((entry) => lastForEvent.get(entry.registration.event) === entry);
}

/**
 * One host's full registration set: the deduplicated three stages plus the
 * additional native events, with each additional registration resolved to the
 * shared stage's script file and budget. This is the set install writes and
 * removal strips, so the two cannot disagree about an event.
 */
export function allRegistrations(hostId: string): LifecycleRegistration[] {
  const lifecycle: HostLifecycle = hostLifecycle(hostId);
  const stages: LifecycleRegistration[] = orderedRegistrations(lifecycle).map(
    ({ registration }) => registration,
  );
  const resolved: LifecycleRegistration[] = additionalRegistrations(hostId).map(
    (additional) => ({
      event: additional.event,
      scriptFile: HOOK_SCRIPT_FILES[additional.stage],
      budget: registrationFor(hostId, additional.stage).budget,
      budgetUnit: registrationFor(hostId, additional.stage).budgetUnit,
      style: additional.style,
    }),
  );
  return [...stages, ...resolved];
}

/** The budget in the declared unit, converted to milliseconds for deadline math. */
export function budgetMilliseconds(registration: LifecycleRegistration): number {
  return registration.budgetUnit === "seconds"
    ? registration.budget * 1000
    : registration.budget;
}

/** The stage a packaged script file serves, resolved from the one shared table. */
export function stageForScriptFile(scriptFile: string): LifecycleStage | undefined {
  return LIFECYCLE_STAGES.find((stage) => HOOK_SCRIPT_FILES[stage] === scriptFile);
}