import { OWNERSHIP_MARKER } from "../write/marker";
import type { LifecycleRegistration, RegistrationStyle } from "./lifecycle-types";

/**
 * The per-host registration shape encoder.
 *
 * It turns one lifecycle registration into the exact nested structure a host's
 * loader expects for its declared shape style, and decodes the same structure
 * back so install, re-install, uninstall and the read-back check all agree on
 * what is this package's own entry.
 *
 * The shapes are the hosts' own contracts, not this package's invention:
 *
 * - `matcher-group` (Claude Code, Codex, CodeBuddy) —
 *   `{"<Event>": [{"hooks": [{"type": "command", "command": "node \"…\"", "timeout": N}]}]}`.
 * - `flat-entry` (Cursor CLI, Copilot CLI) — `{"<event>": {"command": "…", "timeout": N}}`.
 *
 * Getting this wrong is silent: the host loads its config, finds no entry it
 * recognizes, and runs no hook, so the only way to know is to compare what was
 * written against what the host reads. `hostReadsOurEntry` below is that check.
 */

/** The wrapper key every host's event map lives under. */
export const HOOKS_CONTAINER_KEY = "hooks";

/** The execution-type value a matcher-group command entry carries. */
export const HOOK_COMMAND_TYPE = "command";

/**
 * The matcher-group's own key. It is `hooks`, the name every host that speaks
 * this protocol reads — not a name of ours.
 */
export const MATCHER_GROUP_KEY = "hooks";

/** A hook command entry: the execution type, the script path and the budget. */
export interface HookCommandEntry {
  type: string;
  command: string;
  timeout: number;
}

/** One event's matcher group: an array holding exactly one command entry. */
export interface MatcherGroup {
  [MATCHER_GROUP_KEY]: HookCommandEntry[];
}

export type EncodedRegistration = MatcherGroup | HookCommandEntry | HookCommandEntry[];

/**
 * The command string a host executes.
 *
 * A host runs a command entry through a shell, and a bare `.mjs` path is only
 * executable because of its interpreter line — which a shell on Windows does
 * not honour. Every host is therefore given `node "<absolute path>"`: the same
 * shape the reference implementation installs.
 */
export function hookCommand(scriptPath: string): string {
  return `node "${scriptPath}"`;
}

/**
 * Encodes one registration for the style the host declared. The script path is
 * the absolute path the host launches; the budget is the number the host reads
 * in its own declared unit, which the caller has already resolved.
 */
export function encodeRegistration(
  registration: LifecycleRegistration,
  scriptPath: string,
  budgetValue: number,
): EncodedRegistration {
  return encoderFor(registration.style)(scriptPath, budgetValue);
}

function encoderFor(
  style: RegistrationStyle,
): (scriptPath: string, budgetValue: number) => EncodedRegistration {
  switch (style) {
    case "matcher-group":
      return encodeMatcherGroup;
    case "flat-entry":
      return (scriptPath, budgetValue) => commandEntry(scriptPath, budgetValue);
    case "table-array":
      return (scriptPath, budgetValue) => [commandEntry(scriptPath, budgetValue)];
    default:
      return encodeMatcherGroup;
  }
}

function commandEntry(scriptPath: string, budgetValue: number): HookCommandEntry {
  return { type: HOOK_COMMAND_TYPE, command: hookCommand(scriptPath), timeout: budgetValue };
}

/** The style the hook hosts use: one matcher group holding one command entry. */
function encodeMatcherGroup(scriptPath: string, budgetValue: number): MatcherGroup {
  return { [MATCHER_GROUP_KEY]: [commandEntry(scriptPath, budgetValue)] };
}

/**
 * The command entries inside an already-written event value, whatever style it
 * was written in, so a decode does not depend on the writer's style. An event
 * value that is a list is decoded element by element, so a foreign list our
 * entry was appended to still yields both the host's entries and ours.
 */
export function commandEntriesOf(value: unknown): HookCommandEntry[] {
  if (Array.isArray(value)) {
    return value.flatMap((item) => commandEntriesOf(item));
  }
  if (isMatcherGroup(value)) {
    return value[MATCHER_GROUP_KEY].filter(isCommandEntry);
  }
  if (isCommandEntry(value)) {
    return [value];
  }
  return [];
}

/**
 * True when a value is this package's own entry: its command carries the
 * ownership marker. Recognition is by the marker substring rather than by a
 * flag, so an entry written by an older version is still recognized.
 */
export function registrationIsOwned(value: unknown): boolean {
  return commandEntriesOf(value).some((entry) => entry.command.includes(OWNERSHIP_MARKER));
}

/**
 * True when the host, reading `value` in its own declared shape, finds an entry
 * that launches `scriptPath`.
 *
 * This is the post-write check that turns a silent mis-wiring into a reported
 * failure: `encodeRegistration` can only be believed if the host's own reader
 * agrees, and this function is that reader written down.
 */
export function hostReadsOurEntry(
  value: unknown,
  scriptPath: string,
  style: RegistrationStyle,
): boolean {
  return entriesHostWouldRun(value, style).some((entry) => entryNames(entry.command, scriptPath));
}

/** The entries a host of this style would actually launch from one event value. */
export function entriesHostWouldRun(value: unknown, style: RegistrationStyle): HookCommandEntry[] {
  switch (style) {
    case "matcher-group": {
      // A matcher-group host iterates the event's list and reads each element's
      // `hooks` array; a bare object on the event key is not a group it runs.
      const groups: unknown[] = Array.isArray(value) ? value : [value];
      return groups
        .filter(isMatcherGroup)
        .flatMap((group) => group[MATCHER_GROUP_KEY])
        .filter(isCommandEntry);
    }
    case "flat-entry":
      return isCommandEntry(value) ? [value] : [];
    case "table-array":
      return (Array.isArray(value) ? value : [value]).filter(isCommandEntry);
    default:
      return commandEntriesOf(value);
  }
}

/** True when a host-executed command line launches this exact script. */
export function entryNames(command: string, scriptPath: string): boolean {
  return command.includes(`"${scriptPath}"`) || command === scriptPath;
}

function isCommandEntry(value: unknown): value is HookCommandEntry {
  return (
    value !== null &&
    typeof value === "object" &&
    !Array.isArray(value) &&
    typeof (value as HookCommandEntry).command === "string" &&
    typeof (value as HookCommandEntry).timeout === "number"
  );
}

function isMatcherGroup(value: unknown): value is MatcherGroup {
  return (
    value !== null &&
    typeof value === "object" &&
    !Array.isArray(value) &&
    Array.isArray((value as MatcherGroup)[MATCHER_GROUP_KEY])
  );
}