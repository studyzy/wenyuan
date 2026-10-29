import {
  LIFECYCLE_STAGES,
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
 * are written into each host's config verbatim and must not be renamed. All
 * three approved hosts read the same registration shape, so the per-host
 * variation is the config file location (owned by the host adapters), the
 * script file name and the numeric budget.
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
const INTERACTIVE_BUDGET_SECONDS = 10;
const STOP_BUDGET_SECONDS = 30;

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
  // DSH declares NO lifecycle: its wiring is a Cordis plugin
  // (`integrations/dsh-plugin`), which attaches memory on the harness's own
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
  return LIFECYCLE_STAGES.map((stage) => ({ stage, registration: lifecycle[stage] }))
    .filter((entry): entry is { stage: LifecycleStage; registration: LifecycleRegistration } =>
      entry.registration !== undefined,
    );
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