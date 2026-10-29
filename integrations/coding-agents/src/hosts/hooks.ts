import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { HOOKS_SUBDIRECTORY } from "../runtime/layout";
import { backupExistingFile } from "../write/backup";
import { upsertJsoncMember, type JsoncEdit } from "../write/jsonc-edit";
import { OWNERSHIP_MARKER } from "../write/marker";
import {
  asJsonDocument,
  mergeValueIntoContainer,
  type ConfigFormat,
  type JsonDocument,
} from "../write/json-merge";
import {
  encodeRegistration,
  HOOKS_CONTAINER_KEY,
  type EncodedRegistration,
} from "./hook-shape";
import {
  budgetMilliseconds,
  hostLifecycle,
  orderedRegistrations,
} from "./lifecycle";
import type { LifecycleRegistration, RegistrationStyle } from "./lifecycle-types";

/**
 * The three lifecycle hooks install merges into a host config.
 *
 * Each host's lifecycle comes from the one shared declaration, so the event
 * spelling, the packaged script file and the numeric budget are never decided
 * here. This module only turns each declared registration into the host's own
 * registration shape and merges it in non-destructively: a pre-existing file
 * is backed up first, foreign entries and unrelated keys survive, and a second
 * run leaves exactly one registration per event.
 */

export { HOOKS_CONTAINER_KEY };

/** The two interactive event spellings, shared so a host always matches. */
export const INTERACTIVE_EVENTS: readonly string[] = ["SessionStart", "UserPromptSubmit"];

/** The stop event spelling, shared so a host always matches. */
export const STOP_EVENT = "Stop";

export interface HookWriteResult {
  ok: boolean;
  written: boolean;
  messages: string[];
  /** The config path the run touched. */
  configPath: string;
  /** The script path each declared event was written to point at. */
  expected: HostHookExpectation[];
  /** Every event whose entry a conflict or a foreign shape kept us out of. */
  blockedEvents: string[];
}

/** One event this package claims to have registered, and where it points. */
export interface HostHookExpectation {
  event: string;
  scriptPath: string;
  style: RegistrationStyle;
}

/** The host whose lifecycle is installed, resolved from the declaration. */
export type HookWriteInput = HookTarget;

/** The absolute path a host config points at for one declared registration. */
export function hookPathFor(scriptFile: string, runtimeDirectory: string): string {
  return join(runtimeDirectory, HOOKS_SUBDIRECTORY, scriptFile);
}

/** What one host's lifecycle write needs resolved. */
export interface HookTarget {
  hostId: string;
  configPath: string;
  format: ConfigFormat;
  /** The stable staged runtime every written command path points into. */
  runtimeDirectory: string;
}

/**
 * The value one event's key holds: the encoded registration for that host.
 * The budget is converted into the host's declared unit at this boundary so
 * the encoder stays a pure transformation.
 */
function eventValue(
  registration: LifecycleRegistration,
  target: HookTarget,
): EncodedRegistration {
  const budgetValue: number =
    registration.budgetUnit === "seconds"
      ? registration.budget
      : budgetMilliseconds(registration);
  return encodeRegistration(
    registration,
    hookPathFor(registration.scriptFile, target.runtimeDirectory),
    budgetValue,
  );
}

/** One event key path for a host config: the wrapper key plus the event spelling. */
function eventPath(event: string): string[] {
  return [HOOKS_CONTAINER_KEY, event];
}

/**
 * Merges one registration per lifecycle event into a host config. A config
 * that permits comments is edited as text; one that does not is merged as a
 * document. Either way a pre-existing file is backed up first, foreign entries
 * survive, and a second run leaves exactly one registration per event.
 */
export async function installLifecycleHooks(input: HookWriteInput): Promise<HookWriteResult> {
  const registrations = orderedRegistrations(hostLifecycle(input.hostId));
  const expected: HostHookExpectation[] = registrations.map(({ registration }) => ({
    event: registration.event,
    scriptPath: hookPathFor(registration.scriptFile, input.runtimeDirectory),
    style: registration.style,
  }));
  const blockedEvents: string[] = [];
  const before: string | null = await readConfigSource(input.configPath);
  if (before === null) {
    const document: JsonDocument = {};
    for (const { registration } of registrations) {
      mergeValueIntoContainer(
        document,
        eventPath(registration.event),
        eventValue(registration, input),
      );
    }
    await writeConfigSource(input.configPath, `${JSON.stringify(document, null, 2)}\n`);
    return result(true, [], input.configPath, expected, blockedEvents);
  }
  await backupExistingFile(input.configPath);
  let text: string = before;
  if (input.format === "jsonc") {
    for (const { registration } of registrations) {
      // The host's event maps to a nested value, so the member on the event key
      // is written directly. A member already holding one of ours is replaced
      // in place; a foreign member on the same event is left exactly as it is,
      // and reported as blocked rather than silently dropping the registration.
      const edit: JsoncEdit | null = upsertJsoncMember(
        text,
        [HOOKS_CONTAINER_KEY],
        registration.event,
        eventValue(registration, input),
        (memberSource) => memberSource.includes(OWNERSHIP_MARKER),
      );
      if (edit === null) {
        return {
          ok: false,
          written: false,
          messages: [
            `${input.configPath} could not be parsed, so the lifecycle hooks were not added. Add them by hand.`,
          ],
          configPath: input.configPath,
          expected,
          blockedEvents,
        };
      }
      if (edit.conflict === true) {
        blockedEvents.push(registration.event);
      }
      text = edit.text;
    }
  } else {
    const document: JsonDocument = asJsonDocument(text);
    for (const { registration } of registrations) {
      if (!mergeValueIntoContainer(document, eventPath(registration.event), eventValue(registration, input))) {
        blockedEvents.push(registration.event);
      }
    }
    text = `${JSON.stringify(document, null, 2)}\n`;
  }
  if (text === before) {
    return result(false, blockedEvents, input.configPath, expected, blockedEvents);
  }
  await writeConfigSource(input.configPath, text);
  return result(true, blockedEvents, input.configPath, expected, blockedEvents);
}

function result(
  written: boolean,
  messages: string[],
  configPath: string,
  expected: HostHookExpectation[],
  blockedEvents: string[],
): HookWriteResult {
  return { ok: true, written, messages, configPath, expected, blockedEvents };
}

async function readConfigSource(path: string): Promise<string | null> {
  try {
    return await readFile(path, "utf8");
  } catch {
    return null;
  }
}

async function writeConfigSource(path: string, content: string): Promise<void> {
  await mkdir(dirname(path), { recursive: true });
  await writeFile(path, content, { mode: 0o600 });
}