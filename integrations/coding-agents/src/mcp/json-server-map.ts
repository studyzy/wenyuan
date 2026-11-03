import { existsSync } from "node:fs";
import { mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { backupExistingFile } from "../write/backup";
import {
  asJsonDocument,
  deleteAtPath,
  documentIsEmpty,
  isJsonObject,
  pruneEmptyContainers,
  type JsonDocument,
} from "../write/json-merge";
import {
  MCP_SERVERS_KEY,
  MCP_SERVER_NAME,
  containerPathFor,
  locationResolutionFor,
} from "./mechanisms";
import {
  classifyEntry,
  entryIsOwned,
  mcpServerEntry,
  type McpEntryInput,
  type McpResult,
} from "./ownership";

/**
 * The JSON host's registration mechanism.
 *
 * The host reads a server-map object keyed by this package's server name from
 * whichever of its candidate config files it finds first, so install writes the
 * first candidate that already exists and falls back to the highest-priority
 * one; uninstall sweeps every candidate so an entry written into a lower-priority
 * file earlier cannot survive as a dormant duplicate. The document merge reuses
 * the foundation's plain-JSON primitives, and the read is defensive: a missing,
 * unparseable or non-object file reads as an empty object.
 */

export interface JsonServerMapInput extends McpEntryInput {
  /** The host's home directory; candidates resolve relative to it. */
  home: string;
  /** Overrides the declared candidate list, for a caller with its own order. */
  candidates?: readonly string[];
}

/** The declared candidate paths for a host, absolute under a home directory. */
export function candidatePaths(hostId: string, home: string): string[] {
  const resolution = locationResolutionFor(hostId);
  if (!resolution) {
    return [];
  }
  const relative: readonly string[] =
    resolution.kind === "fixed" ? [resolution.path] : resolution.paths;
  return relative.map((candidate) => join(home, candidate));
}

/** The key path from the document root to the host's server-map container. */
function serversPath(hostId: string): string[] {
  const declared: readonly string[] | undefined = containerPathFor(hostId);
  return declared ? [...declared] : [MCP_SERVERS_KEY];
}

/** The server-map container a key path names, created when missing. */
function serversContainer(document: JsonDocument, path: readonly string[]): JsonDocument {
  let current: JsonDocument = document;
  for (const key of path) {
    const next: unknown = current[key];
    if (next !== undefined && next !== null && typeof next === "object" && !Array.isArray(next)) {
      current = next as JsonDocument;
      continue;
    }
    const created: JsonDocument = {};
    current[key] = created;
    current = created;
  }
  return current;
}

/** The first candidate that exists, or the highest-priority candidate. */
export function resolveCandidate(paths: readonly string[]): string | undefined {
  const existing: string | undefined = paths.find((candidate) => existsSync(candidate));
  return existing ?? paths[0];
}

/** Writes the entry into the first existing candidate, or the top candidate. */
export async function registerJsonServerMap(input: JsonServerMapInput): Promise<McpResult> {
  const paths: string[] = (input.candidates ?? candidatePaths(input.hostId, input.home)).slice();
  const target: string | undefined = resolveCandidate(paths);
  if (target === undefined) {
    return {
      ok: false,
      written: false,
      messages: [`no MCP config location was declared for ${input.hostId}.`],
    };
  }
  return writeEntry(target, input);
}

/**
 * Sweeps every candidate and removes this package's entry from each, deleting a
 * server-map container the removal emptied and a config file left holding
 * nothing meaningful.
 */
export async function removeJsonServerMap(input: JsonServerMapInput): Promise<McpResult> {
  const paths: string[] = (input.candidates ?? candidatePaths(input.hostId, input.home)).slice();
  const messages: string[] = [];
  let ok = true;
  for (const candidate of paths) {
    const result: McpResult = await removeEntry(candidate, input.hostId, input.serverPath);
    ok = ok && result.ok;
    messages.push(...result.messages);
  }
  return { ok, written: messages.length > 0, messages };
}

async function writeEntry(configPath: string, input: JsonServerMapInput): Promise<McpResult> {
  const before: string | null = await readSource(configPath);
  const document: JsonDocument = before === null ? {} : asJsonDocument(before);
  const path: string[] = serversPath(input.hostId);
  const servers: JsonDocument = serversContainer(document, path);
  const ownership = classifyEntry(servers[MCP_SERVER_NAME], input.serverPath);
  if (ownership === "foreign") {
    return {
      ok: false,
      written: false,
      messages: [
        `an MCP server named "${MCP_SERVER_NAME}" already exists in ${configPath} and was not created by this package; leaving it untouched. Remove or rename it, then install again.`,
      ],
    };
  }
  servers[MCP_SERVER_NAME] = mcpServerEntry(input);
  const text: string = `${JSON.stringify(document, null, 2)}\n`;
  if (before !== null && before === text) {
    return { ok: true, written: false, messages: [] };
  }
  if (before !== null) {
    await backupExistingFile(configPath);
  }
  await writeSource(configPath, text);
  return { ok: true, written: true, messages: [] };
}

async function removeEntry(configPath: string, hostId: string, serverPath: string): Promise<McpResult> {
  const before: string | null = await readSource(configPath);
  if (before === null) {
    return { ok: true, written: false, messages: [] };
  }
  const document: JsonDocument = asJsonDocument(before);
  const path: string[] = serversPath(hostId);
  const servers: unknown = path.reduce<unknown>(
    (current, key) =>
      current !== null && typeof current === "object" && !Array.isArray(current)
        ? (current as JsonDocument)[key]
        : undefined,
    document,
  );
  if (!isJsonObject(servers) || !entryIsOwned(servers[MCP_SERVER_NAME], serverPath)) {
    return { ok: true, written: false, messages: [] };
  }
  deleteAtPath(document, [...path, MCP_SERVER_NAME]);
  pruneEmptyContainers(document);
  await backupExistingFile(configPath);
  if (documentIsEmpty(document)) {
    await rm(configPath, { force: true });
    return { ok: true, written: true, messages: [] };
  }
  await writeSource(configPath, `${JSON.stringify(document, null, 2)}\n`);
  return { ok: true, written: true, messages: [] };
}

async function readSource(path: string): Promise<string | null> {
  try {
    return await readFile(path, "utf8");
  } catch {
    return null;
  }
}

async function writeSource(path: string, content: string): Promise<void> {
  await mkdir(dirname(path), { recursive: true });
  await writeFile(path, content.endsWith("\n") ? content : `${content}\n`, { mode: 0o600 });
}