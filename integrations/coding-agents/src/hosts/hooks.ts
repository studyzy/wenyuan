import { mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { HOOKS_SUBDIRECTORY, HOOK_FILE_SUFFIX } from "../runtime/layout";
import { backupExistingFile } from "../write/backup";
import {
  insertIntoJsoncArray,
  jsoncDocumentIsEmpty,
  removeFromJsoncArray,
  type JsoncEdit,
} from "../write/jsonc-edit";
import {
  asJsonDocument,
  documentIsEmpty,
  mergeEntryIntoContainer,
  pruneEmptyContainers,
  removeOwnEntriesFromContainer,
  type ConfigFormat,
  type JsonDocument,
} from "../write/json-merge";
import { OWNERSHIP_MARKER } from "../write/marker";

/**
 * The three lifecycle hooks install merges into a host config, and the entry
 * points that hosts launch.
 *
 * The events are declared once, here, and shared by install, uninstall and the
 * packaged entry points. The registration shape is deliberately minimal and
 * carries no per-host variation: the per-host field spellings and budget units
 * for these registrations belong to a later slice, and keeping them in one
 * place means supplying them later touches neither merging, backup nor removal.
 */

export const HOOKS_CONTAINER_KEY = "hooks";

export const LIFECYCLE_EVENTS = [
  { id: "session-start", summary: "a session starts" },
  { id: "pre-prompt", summary: "a user prompt is about to be submitted" },
  { id: "stop", summary: "a session stops" },
] as const;

export type LifecycleEventId = (typeof LIFECYCLE_EVENTS)[number]["id"];

export interface LifecycleHookEntry {
  command: string;
  env?: Record<string, string>;
}

export interface HookWriteResult {
  ok: boolean;
  written: boolean;
  messages: string[];
}

/** One registration: the staged hook's absolute path, plus the runtime's environment. */
export function lifecycleHookEntry(
  eventId: LifecycleEventId,
  runtimeDirectory: string,
  environment?: Record<string, string>,
): LifecycleHookEntry {
  const entry: LifecycleHookEntry = { command: hookPathFor(eventId, runtimeDirectory) };
  if (environment && Object.keys(environment).length > 0) {
    entry.env = { ...environment };
  }
  return entry;
}

/** The command path for one event, inside the stable runtime directory. */
export function hookPathFor(eventId: LifecycleEventId, runtimeDirectory: string): string {
  return join(runtimeDirectory, HOOKS_SUBDIRECTORY, `${eventId}${HOOK_FILE_SUFFIX}`);
}

export interface HookWriteInput {
  configPath: string;
  format: ConfigFormat;
  runtimeDirectory: string;
  environment?: Record<string, string>;
}

/**
 * Merges one registration per lifecycle event into a host config. A config
 * that permits comments is edited as text; one that does not is merged as a
 * document. Either way a pre-existing file is backed up first, foreign entries
 * survive, and a second run leaves exactly one registration per event.
 */
export async function installLifecycleHooks(input: HookWriteInput): Promise<HookWriteResult> {
  const before: string | null = await readConfigSource(input.configPath);
  if (before === null) {
    const document: JsonDocument = {};
    for (const event of LIFECYCLE_EVENTS) {
      mergeEntryIntoContainer(
        document,
        [HOOKS_CONTAINER_KEY, event.id],
        lifecycleHookEntry(event.id, input.runtimeDirectory, input.environment),
      );
    }
    await writeConfigSource(input.configPath, `${JSON.stringify(document, null, 2)}\n`);
    return { ok: true, written: true, messages: [] };
  }
  await backupExistingFile(input.configPath);
  let text: string = before;
  if (input.format === "jsonc") {
    for (const event of LIFECYCLE_EVENTS) {
      const edit: JsoncEdit | null = insertIntoJsoncArray(
        text,
        [HOOKS_CONTAINER_KEY, event.id],
        lifecycleHookEntry(event.id, input.runtimeDirectory, input.environment),
      );
      if (edit === null) {
        return {
          ok: false,
          written: false,
          messages: [
            `${input.configPath} could not be parsed, so the lifecycle hooks were not added. Add them by hand.`,
          ],
        };
      }
      text = edit.text;
    }
  } else {
    const document: JsonDocument = asJsonDocument(text);
    for (const event of LIFECYCLE_EVENTS) {
      mergeEntryIntoContainer(
        document,
        [HOOKS_CONTAINER_KEY, event.id],
        lifecycleHookEntry(event.id, input.runtimeDirectory, input.environment),
      );
    }
    text = `${JSON.stringify(document, null, 2)}\n`;
  }
  if (text === before) {
    return { ok: true, written: false, messages: [] };
  }
  await writeConfigSource(input.configPath, text);
  return { ok: true, written: true, messages: [] };
}

/**
 * Removes this package's own registrations, then deletes the containers the
 * removal emptied and the file itself when nothing meaningful is left.
 */
export async function removeLifecycleHooks(input: {
  configPath: string;
  format: ConfigFormat;
}): Promise<HookWriteResult> {
  const before: string | null = await readConfigSource(input.configPath);
  if (before === null) {
    return { ok: true, written: false, messages: [] };
  }
  await backupExistingFile(input.configPath);
  let text: string = before;
  if (input.format === "jsonc") {
    for (const event of LIFECYCLE_EVENTS) {
      const edit: JsoncEdit | null = removeFromJsoncArray(
        text,
        [HOOKS_CONTAINER_KEY, event.id],
        (elementSource) => elementSource.includes(OWNERSHIP_MARKER),
      );
      if (edit === null) {
        return {
          ok: false,
          written: false,
          messages: [
            `${input.configPath} could not be parsed, so its lifecycle hooks were left alone.`,
          ],
        };
      }
      text = edit.text;
    }
    if (jsoncDocumentIsEmpty(text)) {
      await rm(input.configPath, { force: true });
      return { ok: true, written: true, messages: [] };
    }
  } else {
    const document: JsonDocument = asJsonDocument(text);
    for (const event of LIFECYCLE_EVENTS) {
      removeOwnEntriesFromContainer(document, [HOOKS_CONTAINER_KEY, event.id]);
    }
    pruneEmptyContainers(document);
    if (documentIsEmpty(document)) {
      await rm(input.configPath, { force: true });
      return { ok: true, written: true, messages: [] };
    }
    text = `${JSON.stringify(document, null, 2)}\n`;
  }
  if (text === before) {
    return { ok: true, written: false, messages: [] };
  }
  await writeConfigSource(input.configPath, text);
  return { ok: true, written: true, messages: [] };
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
