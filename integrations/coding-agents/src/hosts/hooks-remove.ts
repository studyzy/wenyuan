import { mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { dirname } from "node:path";
import { backupExistingFile } from "../write/backup";
import { jsoncDocumentIsEmpty, removeJsoncMember, type JsoncEdit } from "../write/jsonc-edit";
import {
  asJsonDocument,
  documentIsEmpty,
  pruneEmptyContainers,
  removeOwnValuesFromContainer,
  type ConfigFormat,
  type JsonDocument,
} from "../write/json-merge";
import { OWNERSHIP_MARKER } from "../write/marker";
import {
  allRegistrations,
  hookContainerPath,
} from "./lifecycle";
import type { HookWriteResult } from "./hooks";

/**
 * Removing the lifecycle hooks install merged in.
 *
 * It strips this package's own entries from exactly the events the host's
 * lifecycle declares and nothing else, deletes an event key or the wrapping
 * container the removal emptied, and deletes a config file left holding
 * nothing meaningful while keeping one that still carries foreign content.
 */

export interface HookRemoveInput {
  hostId: string;
  configPath: string;
  format: ConfigFormat;
}

/** What a removal reports: the shared write result, with nothing to verify. */
function removed(written: boolean, messages: string[] = [], ok = true): HookWriteResult {
  return { ok, written, messages, configPath: "", expected: [], blockedEvents: [] };
}

/**
 * Removes this package's own registrations, then deletes the containers the
 * removal emptied and the file itself when nothing meaningful is left.
 */
export async function removeLifecycleHooks(input: HookRemoveInput): Promise<HookWriteResult> {
  const events: string[] = [...new Set(allRegistrations(input.hostId).map(({ event }) => event))];
  const before: string | null = await readConfigSource(input.configPath);
  if (before === null) {
    return removed(false);
  }
  await backupExistingFile(input.configPath);
  let text: string = before;
  if (input.format === "jsonc") {
    for (const event of events) {
      // The event key holds a nested value, so the member itself is removed
      // when it carries the ownership marker, and the containers the removal
      // emptied are pruned. A foreign member on the same event is left alone.
      const edit: JsoncEdit | null = removeJsoncMember(
        text,
        [...hookContainerPath(input.hostId)],
        event,
        (memberSource) => memberSource.includes(OWNERSHIP_MARKER),
      );
      if (edit === null) {
        return removed(
          false,
          [`${input.configPath} could not be parsed, so its lifecycle hooks were left alone.`],
          false,
        );
      }
      text = edit.text;
    }
    if (jsoncDocumentIsEmpty(text)) {
      await rm(input.configPath, { force: true });
      return removed(true);
    }
  } else {
    const document: JsonDocument = asJsonDocument(text);
    const containerPath: readonly string[] = hookContainerPath(input.hostId);
    for (const event of events) {
      removeOwnValuesFromContainer(document, [...containerPath, event]);
    }
    // A nested event container (ZCode's hooks.events) that the removal emptied
    // takes the whole declared hook container with it: the members install
    // seeded beside the events (the enabled switch, the output cap) are this
    // package's wiring, and leaving them behind would keep the host's hook
    // system on with nothing registered to run.
    if (containerPath.length >= 2) {
      const rootKey: string | undefined = containerPath[0];
      const nextKey: string | undefined = containerPath[1];
      const root: unknown = rootKey === undefined ? undefined : document[rootKey];
      const eventsContainer: unknown =
        root !== null && typeof root === "object" && nextKey !== undefined
          ? (root as JsonDocument)[nextKey]
          : undefined;
      const emptied: boolean =
        eventsContainer === undefined ||
        eventsContainer === null ||
        typeof eventsContainer !== "object" ||
        Array.isArray(eventsContainer) ||
        Object.keys(eventsContainer as JsonDocument).length === 0;
      if (emptied && rootKey !== undefined) {
        delete document[rootKey];
      }
    }
    pruneEmptyContainers(document);
    if (documentIsEmpty(document)) {
      await rm(input.configPath, { force: true });
      return removed(true);
    }
    text = `${JSON.stringify(document, null, 2)}\n`;
  }
  if (text === before) {
    return removed(false);
  }
  await writeConfigSource(input.configPath, text);
  return removed(true);
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