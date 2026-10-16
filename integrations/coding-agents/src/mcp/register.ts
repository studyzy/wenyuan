import { mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { dirname } from "node:path";
import {
  jsoncDocumentIsEmpty,
  removeJsoncMember,
  upsertJsoncMember,
  type JsoncEdit,
} from "../write/jsonc-edit";
import { findMember, rootObjectRange } from "../write/jsonc-scan";
import { backupExistingFile } from "../write/backup";
import {
  asJsonDocument,
  deleteAtPath,
  documentIsEmpty,
  isJsonObject,
  pruneEmptyContainers,
  type JsonDocument,
} from "../write/json-merge";
import { OWNERSHIP_MARKER, valueCarriesMarker } from "../write/marker";

/**
 * The MCP server registration each host gets.
 *
 * Every host launches the same packaged server, so every registration sets the
 * caller-identifying variable to the host's own name: that is how the server
 * attributes what it is asked to remember. The registration points at the
 * staged runtime and never at the location the command ran from, and a
 * same-named entry this package did not create is never overwritten and never
 * removed.
 */

/** Wire contract: the server reads this to attribute the caller. */
export const MCP_HARNESS_ENV = "HINDSIGHT_MCP_HARNESS";

/** The container hosts read MCP server entries from. */
export const MCP_SERVERS_KEY = "mcpServers";

/** This package's server name, which doubles as the ownership marker. */
export const MCP_SERVER_NAME = OWNERSHIP_MARKER;

/** The launcher a host execs; the server path is its first argument. */
export const MCP_LAUNCHER_COMMAND = "node";

/** A host config is either plain JSON or JSON that permits comments. */
import type { ConfigFormat } from "../write/json-merge";

export type { ConfigFormat };

export interface McpServerEntry {
  command: string;
  args: string[];
  env?: Record<string, string>;
}

export type McpOwnership = "absent" | "owned" | "foreign";

export interface McpResult {
  ok: boolean;
  written: boolean;
  messages: string[];
}

export interface McpTarget {
  configPath: string;
  format: ConfigFormat;
  hostId: string;
}

export interface RegisterInput extends McpTarget {
  serverPath: string;
  environment?: Record<string, string>;
}

/** Builds the stdio entry, naming the calling host in its environment. */
export function mcpServerEntry(
  serverPath: string,
  hostId: string,
  environment: Record<string, string> = {},
): McpServerEntry {
  return {
    command: MCP_LAUNCHER_COMMAND,
    args: [serverPath],
    env: { ...environment, [MCP_HARNESS_ENV]: hostId },
  };
}

function isOwnedSource(source: string): boolean {
  return source.includes(OWNERSHIP_MARKER);
}

/**
 * What is already registered under this package's name, so an install can
 * refuse a foreign entry before it writes anything.
 */
export async function inspectMcpServer(target: McpTarget): Promise<McpOwnership> {
  const source: string | null = await readConfigSource(target.configPath);
  if (source === null) {
    return "absent";
  }
  const existing: string | null = existingEntrySource(source, target);
  if (existing === null) {
    return "absent";
  }
  return isOwnedSource(existing) ? "owned" : "foreign";
}

/** Writes the registration, or refuses when the name is taken by a foreign entry. */
export async function registerMcpServer(input: RegisterInput): Promise<McpResult> {
  const ownership: McpOwnership = await inspectMcpServer(input);
  if (ownership === "foreign") {
    return {
      ok: false,
      written: false,
      messages: [
        `an MCP server named "${MCP_SERVER_NAME}" already exists in ${input.configPath} and was not created by this package; leaving it untouched. Remove or rename it, then install again.`,
      ],
    };
  }
  const entry: McpServerEntry = mcpServerEntry(
    input.serverPath,
    input.hostId,
    input.environment,
  );
  const before: string | null = await readConfigSource(input.configPath);
  if (before === null) {
    await writeConfigSource(input.configPath, `${JSON.stringify({ [MCP_SERVERS_KEY]: { [MCP_SERVER_NAME]: entry } }, null, 2)}\n`);
    return { ok: true, written: true, messages: [] };
  }
  await backupExistingFile(input.configPath);
  const edited: string | null = await applyEdit(input, before, async (source) =>
    input.format === "jsonc"
      ? upsertJsoncMember(source, [MCP_SERVERS_KEY], MCP_SERVER_NAME, entry, isOwnedSource)
      : upsertJsonMember(source, MCP_SERVER_NAME, entry),
  );
  if (edited === null) {
    return {
      ok: false,
      written: false,
      messages: [
        `${input.configPath} could not be parsed, so the MCP server entry was not added. Add it by hand.`,
      ],
    };
  }
  if (edited === before) {
    return { ok: true, written: false, messages: [] };
  }
  if (edited === "") {
    // The removal emptied the file, so the husk goes away rather than stays.
    await removeConfigSource(input.configPath);
    return { ok: true, written: true, messages: [] };
  }
  await writeConfigSource(input.configPath, edited);
  return { ok: true, written: true, messages: [] };
}

/** Removes the registration, leaving a foreign entry of the same name alone. */
export async function removeMcpServer(target: McpTarget): Promise<McpResult> {
  const before: string | null = await readConfigSource(target.configPath);
  if (before === null) {
    return { ok: true, written: false, messages: [] };
  }
  await backupExistingFile(target.configPath);
  const edited: string | null = await applyEdit(target, before, async (source) =>
    target.format === "jsonc"
      ? removeJsoncMember(source, [MCP_SERVERS_KEY], MCP_SERVER_NAME, isOwnedSource)
      : removeJsonMember(source, MCP_SERVER_NAME),
  );
  if (edited === null) {
    return {
      ok: false,
      written: false,
      messages: [`${target.configPath} could not be parsed, so its MCP server entry was left alone.`],
    };
  }
  if (edited === before) {
    return { ok: true, written: false, messages: [] };
  }
  if (edited === "") {
    await removeConfigSource(target.configPath);
    return { ok: true, written: true, messages: [] };
  }
  await writeConfigSource(target.configPath, edited);
  return { ok: true, written: true, messages: [] };
}

async function applyEdit(
  target: McpTarget,
  source: string,
  edit: (source: string) => Promise<JsoncEdit | null> | JsoncEdit | null,
): Promise<string | null> {
  const result: JsoncEdit | null = await edit(source);
  if (result === null) {
    return null;
  }
  if (!result.changed) {
    return source;
  }
  const text: string = result.text;
  if (target.format === "jsonc") {
    return jsoncDocumentIsEmpty(text) ? "" : text;
  }
  return text;
}

function upsertJsonMember(source: string, key: string, value: unknown): JsoncEdit | null {
  const document: JsonDocument = asJsonDocument(source);
  if (!isJsonObject(document[MCP_SERVERS_KEY])) {
    document[MCP_SERVERS_KEY] = {};
  }
  const servers: JsonDocument = document[MCP_SERVERS_KEY] as JsonDocument;
  const existing: unknown = servers[key];
  if (existing !== undefined && !valueCarriesMarker(existing)) {
    return { text: source, changed: false, conflict: true };
  }
  servers[key] = value;
  return { text: `${JSON.stringify(document, null, 2)}\n`, changed: true };
}

function removeJsonMember(source: string, key: string): JsoncEdit | null {
  const document: JsonDocument = asJsonDocument(source);
  const servers: unknown = document[MCP_SERVERS_KEY];
  if (!isJsonObject(servers) || servers[key] === undefined) {
    return { text: source, changed: false };
  }
  if (!valueCarriesMarker(servers[key])) {
    return { text: source, changed: false, conflict: true };
  }
  deleteAtPath(document, [MCP_SERVERS_KEY, key]);
  pruneEmptyContainers(document);
  return { text: documentIsEmpty(document) ? "" : `${JSON.stringify(document, null, 2)}\n`, changed: true };
}

/** The source text of an existing entry under this package's name. */
function existingEntrySource(source: string, target: McpTarget): string | null {
  if (target.format === "jsonc") {
    const root = rootObjectRange(source);
    if (!root) {
      return null;
    }
    const servers = findMember(source, root, MCP_SERVERS_KEY);
    if (!servers || source[servers.value.start] !== "{") {
      return null;
    }
    const member = findMember(source, servers.value, MCP_SERVER_NAME);
    if (!member) {
      return null;
    }
    // The value alone decides ownership: the key is this package's name and
    // carries the marker whether or not the entry is ours.
    return source.slice(member.value.start, member.value.end);
  }
  const document: JsonDocument = asJsonDocument(source);
  const servers: unknown = document[MCP_SERVERS_KEY];
  if (!isJsonObject(servers) || servers[MCP_SERVER_NAME] === undefined) {
    return null;
  }
  return JSON.stringify(servers[MCP_SERVER_NAME]);
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
  await writeFile(path, content.endsWith("\n") ? content : `${content}\n`, { mode: 0o600 });
}

async function removeConfigSource(path: string): Promise<void> {
  await rm(path, { force: true });
}
