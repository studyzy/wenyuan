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
    const result: McpResult = await removeEntry(candidate, input.serverPath);
    ok = ok && result.ok;
    messages.push(...result.messages);
  }
  return { ok, written: messages.length > 0, messages };
}

async function writeEntry(configPath: string, input: JsonServerMapInput): Promise<McpResult> {
  const before: string | null = await readSource(configPath);
  const document: JsonDocument = before === null ? {} : asJsonDocument(before);
  const servers: JsonDocument = isJsonObject(document[MCP_SERVERS_KEY])
    ? (document[MCP_SERVERS_KEY] as JsonDocument)
    : {};
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
  document[MCP_SERVERS_KEY] = servers;
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

async function removeEntry(configPath: string, serverPath: string): Promise<McpResult> {
  const before: string | null = await readSource(configPath);
  if (before === null) {
    return { ok: true, written: false, messages: [] };
  }
  const document: JsonDocument = asJsonDocument(before);
  const servers: unknown = document[MCP_SERVERS_KEY];
  if (!isJsonObject(servers) || !entryIsOwned(servers[MCP_SERVER_NAME], serverPath)) {
    return { ok: true, written: false, messages: [] };
  }
  deleteAtPath(document, [MCP_SERVERS_KEY, MCP_SERVER_NAME]);
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