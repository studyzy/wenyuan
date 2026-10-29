import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname } from "node:path";
import { valueCarriesMarker } from "./marker";

/** A JSON document whose top level is an object. */
export type JsonDocument = Record<string, unknown>;

/**
 * How a host config may be written: plain JSON, or JSON that permits comments
 * and trailing commas and has to be edited as text.
 */
export type ConfigFormat = "json" | "jsonc";

export interface JsonFileRead {
  /** False for a missing file, which is read as an empty document. */
  existed: boolean;
  document: JsonDocument;
}

/**
 * Reads a host config defensively: a missing file, a file that will not parse
 * and a well-formed file whose top level is not an object all read as an empty
 * document, so a broken config is never merged into and written back out.
 */
export async function readJsonFile(path: string): Promise<JsonFileRead> {
  let source: string;
  try {
    source = await readFile(path, "utf8");
  } catch {
    return { existed: false, document: {} };
  }
  return { existed: true, document: asJsonDocument(source) };
}

/** Parses JSON, keeping only a top-level object and treating anything else as empty. */
export function asJsonDocument(source: string): JsonDocument {
  let parsed: unknown;
  try {
    parsed = JSON.parse(source);
  } catch {
    return {};
  }
  return isJsonObject(parsed) ? parsed : {};
}

export function isJsonObject(value: unknown): value is JsonDocument {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

/**
 * Reads the array a list of keys names, returning an empty array when any step
 * is missing or is not the expected shape. Nothing is created.
 */
export function containerAt(document: JsonDocument, path: readonly string[]): unknown[] {
  let current: unknown = document;
  for (const key of path) {
    if (!isJsonObject(current)) {
      return [];
    }
    current = current[key];
  }
  return Array.isArray(current) ? current : [];
}

function ensureContainer(document: JsonDocument, path: readonly string[]): unknown[] {
  let current: JsonDocument = document;
  for (const key of path.slice(0, -1)) {
    const next: unknown = current[key];
    if (!isJsonObject(next)) {
      const created: JsonDocument = {};
      current[key] = created;
      current = created;
    } else {
      current = next;
    }
  }
  const last: string | undefined = path[path.length - 1];
  if (last === undefined) {
    return [];
  }
  const existing: unknown = current[last];
  if (!Array.isArray(existing)) {
    const created: unknown[] = [];
    current[last] = created;
    return created;
  }
  return existing;
}

/**
 * Merges one value into the container a key path names: the package's own
 * entries are filtered out first, so foreign entries survive and a second run
 * leaves exactly one entry of ours behind.
 */
export function mergeEntryIntoContainer(
  document: JsonDocument,
  path: readonly string[],
  entry: unknown,
): void {
  const container: unknown[] = ensureContainer(document, path);
  const kept: unknown[] = container.filter((item) => !valueCarriesMarker(item));
  kept.push(entry);
  container.length = 0;
  container.push(...kept);
}

/**
 * Merges one whole value onto the member a key path names, rather than into an
 * array. A host registration whose own shape is a nested object (a matcher
 * group holding a command entry) lives on the event key directly, so this sets
 * the key to the value: a key already holding one of ours is replaced in place,
 * a key holding a foreign array of entries keeps those entries and receives
 * ours appended after them, and any other foreign value is left alone.
 *
 * Returns false when a foreign value blocked the write, so the caller can
 * report a registration that will never run instead of claiming one.
 */
export function mergeValueIntoContainer(
  document: JsonDocument,
  path: readonly string[],
  value: unknown,
): boolean {
  ensureObjectMembers(document, path.slice(0, -1));
  const key: string | undefined = path[path.length - 1];
  if (key === undefined) {
    return false;
  }
  let parent: JsonDocument = document;
  for (const step of path.slice(0, -1)) {
    const next: unknown = parent[step];
    if (!isJsonObject(next)) {
      break;
    }
    parent = next;
  }
  const existing: unknown = parent[key];
  if (Array.isArray(existing)) {
    // A list on this key is the host's own shape or one install already
    // appended to: drop any of ours and append ours after the host's entries,
    // so both survive and a re-run still leaves exactly one of ours.
    const kept: unknown[] = existing.filter((item) => !valueCarriesMarker(item));
    existing.length = 0;
    existing.push(...kept, value);
    return true;
  }
  if (existing !== undefined && !valueCarriesMarker(existing)) {
    // A foreign registration of any other shape has no room for a second entry
    // and is left exactly as it is; the caller reports it as blocked.
    return false;
  }
  parent[key] = value;
  return true;
}

/** Ensures every object on a key path exists, creating the missing ones. */
function ensureObjectMembers(document: JsonDocument, path: readonly string[]): void {
  let current: JsonDocument = document;
  for (const key of path) {
    const next: unknown = current[key];
    if (!isJsonObject(next)) {
      const created: JsonDocument = {};
      current[key] = created;
      current = created;
    } else {
      current = next;
    }
  }
}

/**
 * Removes this package's own value on the member a key path names, whether the
 * member holds our value directly or a foreign array we appended ours into.
 * Returns how many values went away.
 */
export function removeOwnValuesFromContainer(
  document: JsonDocument,
  path: readonly string[],
): number {
  const key: string | undefined = path[path.length - 1];
  if (key === undefined) {
    return 0;
  }
  let parent: JsonDocument = document;
  for (const step of path.slice(0, -1)) {
    const next: unknown = parent[step];
    if (!isJsonObject(next)) {
      return 0;
    }
    parent = next;
  }
  const existing: unknown = parent[key];
  if (existing === undefined) {
    return 0;
  }
  if (Array.isArray(existing)) {
    // A foreign list the install appended our entry to: only our elements go,
    // and a list left empty is deleted with the key.
    const kept: unknown[] = existing.filter((item) => !valueCarriesMarker(item));
    const removed: number = existing.length - kept.length;
    if (removed === 0) {
      return 0;
    }
    if (kept.length === 0) {
      delete parent[key];
    } else {
      existing.length = 0;
      existing.push(...kept);
    }
    return removed;
  }
  if (!valueCarriesMarker(existing)) {
    return 0;
  }
  delete parent[key];
  return 1;
}

/**
 * Removes every entry of ours from the container a key path names, then
 * deletes the containers that the removal emptied and returns how many
 * entries went away.
 */
export function removeOwnEntriesFromContainer(
  document: JsonDocument,
  path: readonly string[],
): number {
  const container: unknown[] = containerAt(document, path);
  if (container.length === 0) {
    return 0;
  }
  const kept: unknown[] = container.filter((item) => !valueCarriesMarker(item));
  const removed: number = container.length - kept.length;
  if (removed === 0) {
    return 0;
  }
  if (kept.length === 0) {
    deleteAtPath(document, path);
  } else {
    container.length = 0;
    container.push(...kept);
  }
  return removed;
}

/**
 * Deletes the value a key path names, then every container that the deletion
 * emptied, so no empty array or empty object is left behind.
 */
export function deleteAtPath(document: JsonDocument, path: readonly string[]): void {
  const trail: { parent: JsonDocument; key: string }[] = [];
  let current: JsonDocument = document;
  for (const key of path) {
    trail.push({ parent: current, key });
    const next: unknown = current[key];
    if (!isJsonObject(next)) {
      break;
    }
    current = next;
  }
  for (let depth = trail.length - 1; depth >= 0; depth -= 1) {
    const step: { parent: JsonDocument; key: string } | undefined = trail[depth];
    if (!step) {
      break;
    }
    delete step.parent[step.key];
    // Keep climbing only while the deletion emptied a nested container; the
    // root document itself is left for the caller to judge.
    if (depth === 0 || Object.keys(step.parent).length > 0) {
      break;
    }
  }
}

/** True when a document holds nothing at all, so the file itself is a husk. */
export function documentIsEmpty(document: JsonDocument): boolean {
  return Object.keys(document).length === 0;
}

/** Drops keys whose value is an empty array or an empty object. */
export function pruneEmptyContainers(document: JsonDocument): void {
  for (const [key, value] of Object.entries(document)) {
    if (Array.isArray(value) && value.length === 0) {
      delete document[key];
    } else if (isJsonObject(value) && Object.keys(value).length === 0) {
      delete document[key];
    }
  }
}

/**
 * Seeds a schema-version field only when the host config does not carry one;
 * an existing value is the host's own and is left exactly as it is.
 */
export function seedSchemaVersion(
  document: JsonDocument,
  key: string,
  version: string,
): void {
  const existing: unknown = document[key];
  if (existing === undefined || existing === null) {
    document[key] = version;
  }
}

/**
 * Writes a document as pretty-printed JSON with a trailing newline, creating
 * any missing parent directory. Per-user state is written owner-only.
 */
export async function writeJsonFile(
  path: string,
  document: JsonDocument,
  mode: number = 0o600,
): Promise<void> {
  await mkdir(dirname(path), { recursive: true });
  await writeFile(path, `${JSON.stringify(document, null, 2)}\n`, { mode });
}
