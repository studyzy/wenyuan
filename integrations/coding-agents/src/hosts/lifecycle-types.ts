/**
 * The types the per-host lifecycle declaration is written in.
 *
 * They are kept in their own module so the declaration and its consumers agree
 * on one vocabulary without the declaration file having to carry them: the
 * installer, the shape encoder and the packaged entry points all import these.
 */

/** The three lifecycle stages, named once and shared by every consumer. */
export type LifecycleStage = "session-start" | "pre-prompt" | "stop";

/** The fixed order the stages are declared and processed in. */
export const LIFECYCLE_STAGES: readonly LifecycleStage[] = [
  "session-start",
  "pre-prompt",
  "stop",
];

/**
 * How a registration style reads its numeric budget. The host's own field name
 * is the same either way; only the unit the number is expressed in differs, so
 * the same wall-clock budget yields a different number under each.
 */
export type BudgetUnit = "seconds" | "milliseconds";

/**
 * The registration shape a host's loader expects. The first-batch hosts use
 * the matcher-group style; the other styles are declared so a host with a
 * different loader is distinguishable without changing any other field.
 */
/**
 * The registration shape a host's loader expects. The first-batch hosts use
 * the matcher-group style; the other styles are declared so a host with a
 * different loader is distinguishable without changing any other field.
 *
 * - `matcher-group` - a list of groups, each carrying a command-entry list.
 * - `flat-entry` - one command entry object (type, command, timeout).
 * - `plain-entry` - one object holding only command and timeout, no execution
 *   type: the shape a host whose loader reads just the command reads.
 * - `table-array` - a list of command entries, one element per registration.
 * - `process-entry` - the matcher-group nesting, but the entry is an argv
 *   (type "process", a bare "node" command, the script under args) because the
 *   host spawns hooks without a shell, so a quoted command string would be
 *   looked up verbatim as one executable name and never run.
 */
export type RegistrationStyle =
  | "matcher-group"
  | "flat-entry"
  | "plain-entry"
  | "table-array"
  | "process-entry";

/**
 * One lifecycle registration for one host: the host's own event spelling, the
 * packaged hook script file to execute, the budget the host enforces, and the
 * unit and style that say how the host reads both.
 */
export interface LifecycleRegistration {
  /** The host-recognized lifecycle event name, written into its config verbatim. */
  event: string;
  /** The packaged hook script file a host launches, relative to the hooks directory. */
  scriptFile: string;
  /** The budget the host enforces on the hook, in the declared unit. */
  budget: number;
  /** How the budget number is read; hosts that do not declare otherwise use seconds. */
  budgetUnit: BudgetUnit;
  /** The registration shape the host's loader expects. */
  style: RegistrationStyle;
}

/** One host's ordered lifecycle: every stage mapped to its registration. */
export interface HostLifecycle {
  readonly [stage: string]: LifecycleRegistration;
}

/**
 * One native host event beyond the shared three-stage lifecycle, registered
 * beside it. The host names the event and which shared stage's packaged script
 * and budget the registration reuses, so a native event can never point at a
 * script the stage bins do not ship.
 */
export interface AdditionalRegistration {
  /** The host-recognized native event name, written into its config verbatim. */
  event: string;
  /** The shared stage whose packaged script file and budget this reuses. */
  stage: LifecycleStage;
  /** The registration shape the host's loader expects for this event. */
  style: RegistrationStyle;
}