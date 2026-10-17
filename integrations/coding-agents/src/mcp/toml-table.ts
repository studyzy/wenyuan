import { mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { backupExistingFile } from "../write/backup";
import {
  MCP_ENTRY_MEMBERS,
  MCP_HARNESS_ENV,
  MCP_LAUNCHER_COMMAND,
  MCP_SERVER_NAME,
  MCP_TOML_CONTAINER_KEY,
  locationResolutionFor,
} from "./mechanisms";
import type { McpEntryInput, McpResult } from "./ownership";

/**
 * The TOML host's registration mechanism.
 *
 * The host reads a table named after this package's server under its
 * server-container key. The edit is a block edit rather than a whole-document
 * round-trip: any previous table of ours — including its sub-tables — is
 * stripped, the rewritten block is appended, and everything that is not ours is
 * preserved byte for byte. The file is written only when the resulting text
 * differs.
 */

export interface TomlTableInput extends McpEntryInput {
  /** The host's home directory; the declared config path resolves under it. */
  home: string;
  /** Overrides the declared config path, for a caller that already resolved it. */
  configPath?: string;
}

/** The declared TOML config path for a host, absolute under a home directory. */
export function tomlConfigPath(hostId: string, home: string): string | undefined {
  const resolution = locationResolutionFor(hostId);
  if (!resolution || resolution.kind !== "fixed" || resolution.path === "") {
    return undefined;
  }
  return join(home, resolution.path);
}

/**
 * Strips the previous table of ours — the `[<container>.<server>]` table and
 * every `[<container>.<server>.*]` sub-table — from the TOML text, leaving the
 * remaining lines exactly as they were.
 */
export function stripOurTable(source: string, container: string): string {
  const header: string = `${container}.${MCP_SERVER_NAME}`;
  const lines: string[] = source.split("\n");
  const kept: string[] = [];
  let skipping = false;
  for (const line of lines) {
    const table: string | null = tableHeader(line);
    if (table !== null) {
      skipping = table === header || table.startsWith(`${header}.`);
    }
    if (!skipping) {
      kept.push(line);
    }
  }
  return kept.join("\n");
}

/** The dotted table name a TOML table header line declares, or null. */
function tableHeader(line: string): string | null {
  const match = /^\s*\[\[?\s*([^\]]+?)\s*\]\]?\s*$/.exec(line);
  return match?.[1] ?? null;
}

/** Renders this package's table with the shared entry members inline. */
export function renderOurTable(input: McpEntryInput): string {
  const env: Record<string, string> = {
    ...(input.environment ?? {}),
    [MCP_HARNESS_ENV]: input.hostId,
  };
  const envPairs: string = Object.entries(env)
    .map(([name, value]) => `${tomlKey(name)} = ${tomlString(value)}`)
    .join(", ");
  return [
    `[${MCP_TOML_CONTAINER_KEY}.${MCP_SERVER_NAME}]`,
    `${MCP_ENTRY_MEMBERS.command} = ${tomlString(MCP_LAUNCHER_COMMAND)}`,
    `${MCP_ENTRY_MEMBERS.args} = [${tomlString(input.serverPath)}]`,
    `${MCP_ENTRY_MEMBERS.environment} = { ${envPairs} }`,
    "",
  ].join("\n");
}

function tomlString(value: string): string {
  return `"${value.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

function tomlKey(value: string): string {
  return /^[A-Za-z0-9_-]+$/.test(value) ? value : tomlString(value);
}

/**
 * Apply the table edit to TOML text: strip any previous table of ours and
 * append the rewritten block. Returns the new text; unchanged text is returned
 * verbatim so the caller can skip the write.
 */
export function applyTomlEdit(source: string, input: McpEntryInput): string {
  const stripped: string = stripOurTable(source, MCP_TOML_CONTAINER_KEY);
  const body: string = stripped.replace(/\s*$/, "");
  const prefix: string = body === "" ? "" : `${body}\n\n`;
  return `${prefix}${renderOurTable(input)}`;
}

/** Writes the table, or leaves the file untouched when the text already matches. */
export async function registerTomlTable(input: TomlTableInput): Promise<McpResult> {
  const configPath: string | undefined =
    input.configPath ?? tomlConfigPath(input.hostId, input.home);
  if (configPath === undefined) {
    return {
      ok: false,
      written: false,
      messages: [`no TOML config location was declared for ${input.hostId}.`],
    };
  }
  const before: string | null = await readSource(configPath);
  const text: string = applyTomlEdit(before ?? "", input);
  if (before !== null && before === text) {
    return { ok: true, written: false, messages: [] };
  }
  if (before !== null) {
    await backupExistingFile(configPath);
  }
  await mkdir(dirname(configPath), { recursive: true });
  await writeFile(configPath, text.endsWith("\n") ? text : `${text}\n`, { mode: 0o600 });
  return { ok: true, written: true, messages: [] };
}

/** Removes this package's table, deleting a file the removal left empty. */
export async function removeTomlTable(input: TomlTableInput): Promise<McpResult> {
  const configPath: string | undefined =
    input.configPath ?? tomlConfigPath(input.hostId, input.home);
  if (configPath === undefined) {
    return { ok: true, written: false, messages: [] };
  }
  const before: string | null = await readSource(configPath);
  if (before === null) {
    return { ok: true, written: false, messages: [] };
  }
  const stripped: string = stripOurTable(before, MCP_TOML_CONTAINER_KEY);
  if (stripped === before) {
    return { ok: true, written: false, messages: [] };
  }
  await backupExistingFile(configPath);
  if (stripped.trim() === "") {
    await rm(configPath, { force: true });
    return { ok: true, written: true, messages: [] };
  }
  await writeFile(configPath, stripped.endsWith("\n") ? stripped : `${stripped}\n`, { mode: 0o600 });
  return { ok: true, written: true, messages: [] };
}

async function readSource(path: string): Promise<string | null> {
  try {
    return await readFile(path, "utf8");
  } catch {
    return null;
  }
}