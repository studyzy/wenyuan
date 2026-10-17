import { OWNERSHIP_MARKER } from "../write/marker";
import type { LifecycleRegistration, RegistrationStyle } from "./lifecycle-types";

/**
 * The per-host registration shape encoder.
 *
 * It turns one lifecycle registration into the exact nested structure a host's
 * loader expects for its declared shape style, and decodes the same structure
 * back so install, re-install and uninstall all agree on what is this
 * package's own entry.
 *
 * All three approved hosts read the matcher-group style: the host's event maps
 * to a matcher group that holds an array with exactly one command entry, and
 * that entry names the execution type, the packaged script path and the host's
 * budget. The other declared styles each get their own encoder, so adding a
 * host with a different loader adds an encoder rather than a growing
 * conditional in the caller.
 */

/** The wrapper key every host's event map lives under. */
export const HOOKS_CONTAINER_KEY = "hooks";

/** The execution-type value a matcher-group command entry carries. */
export const HOOK_COMMAND_TYPE = "command";

/** The matcher-group wrapper key inside one event's value. */
export const MATCHER_GROUP_KEY = "matchers";

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
    case "argv-split":
      return encodeArgvSplit;
    case "table-array":
      return (scriptPath, budgetValue) => [commandEntry(scriptPath, budgetValue)];
    default:
      return encodeMatcherGroup;
  }
}

function commandEntry(scriptPath: string, budgetValue: number): HookCommandEntry {
  return { type: HOOK_COMMAND_TYPE, command: scriptPath, timeout: budgetValue };
}

/** The style all three approved hosts use. */
function encodeMatcherGroup(scriptPath: string, budgetValue: number): MatcherGroup {
  return { [MATCHER_GROUP_KEY]: [commandEntry(scriptPath, budgetValue)] };
}

/** A style whose entry splits the path into an argv on whitespace. */
function encodeArgvSplit(scriptPath: string, budgetValue: number): HookCommandEntry {
  return { type: HOOK_COMMAND_TYPE, command: scriptPath, timeout: budgetValue };
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
 * True when a value is this package's own entry: its command path carries the
 * ownership marker. Recognition is by the marker substring rather than by a
 * flag, so an entry written by an older version is still recognized.
 */
export function registrationIsOwned(value: unknown): boolean {
  return commandEntriesOf(value).some((entry) => entry.command.includes(OWNERSHIP_MARKER));
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