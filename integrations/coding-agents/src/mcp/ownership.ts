import { OWNERSHIP_MARKER } from "../write/marker";
import { MCP_ENTRY_MEMBERS, MCP_HARNESS_ENV, MCP_LAUNCHER_COMMAND, MCP_SERVER_NAME } from "./mechanisms";

/**
 * The one shared server-entry shape and the one ownership test.
 *
 * Every mechanism emits the same entry: the launcher command, the argv whose
 * first element names the packaged server entry point, and the single
 * harness-attribution environment member. Ownership is decided by that exact
 * shape and never by the server name alone, so a foreign entry that merely
 * shares the name is never overwritten on install and never removed on
 * uninstall, and install, re-install and uninstall always agree.
 */

export interface McpServerEntry {
  command: string;
  args: string[];
  env?: Record<string, string>;
}

export interface McpEntryInput {
  /** The absolute staged path of the packaged server entry point. */
  serverPath: string;
  /** The host that owns this entry; becomes the harness value. */
  hostId: string;
  /** Deployment-contract values the packaged runtime reads at launch. */
  environment?: Record<string, string>;
}

/** Builds the stdio entry, naming the calling host in its environment. */
export function mcpServerEntry(input: McpEntryInput): McpServerEntry {
  return {
    command: MCP_LAUNCHER_COMMAND,
    args: [input.serverPath],
    env: { ...(input.environment ?? {}), [MCP_HARNESS_ENV]: input.hostId },
  };
}

/** The packaged server's file name, which is what identifies our entry. */
export const PACKAGED_SERVER_FILE_NAME = "mcp-server.mjs";

/** The directory the staged server sits in, which identifies its package. */
export const PACKAGED_RUNTIME_DIRECTORY_NAME = OWNERSHIP_MARKER;

/**
 * True when an existing entry is this package's, judged by its shape rather
 * than by its exact path:
 *
 * - the command is the node launcher, exactly as this package writes it;
 * - the FIRST argv element is the packaged server file, by name, inside the
 *   package's own runtime directory.
 *
 * Matching on the directory name instead of the absolute staged path is what
 * lets a re-install and an uninstall recognize an entry written before the home
 * directory, the package location or the staging layout changed. Comparing full
 * paths made every such entry permanently foreign: it was neither repaired by a
 * re-install nor removed by an uninstall.
 *
 * Requiring the first argv element — not any element — keeps an operator's own
 * launcher that merely mentions our path (`node wrapper.mjs <our path>`) out of
 * the ownership set, so install never overwrites it and uninstall never deletes
 * it.
 */
export function entryIsOwned(value: unknown, serverPath?: string): boolean {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const entry = value as Record<string, unknown>;
  if (entry[MCP_ENTRY_MEMBERS.command] !== MCP_LAUNCHER_COMMAND) {
    return false;
  }
  const args: unknown = entry[MCP_ENTRY_MEMBERS.args];
  if (!Array.isArray(args) || args.length === 0) {
    return false;
  }
  const first: unknown = args[0];
  if (typeof first !== "string") {
    return false;
  }
  if (serverPath !== undefined && first === serverPath) {
    return true;
  }
  return (
    basename(first) === PACKAGED_SERVER_FILE_NAME &&
    first.split(/[\\/]/).includes(PACKAGED_RUNTIME_DIRECTORY_NAME)
  );
}

function basename(path: string): string {
  const parts: string[] = path.split(/[\\/]/);
  return parts[parts.length - 1] ?? "";
}

export type McpOwnership = "absent" | "owned" | "foreign";

/** Reads the entry under the package's name and classifies its ownership. */
export function classifyEntry(existing: unknown, serverPath: string): McpOwnership {
  if (existing === undefined) {
    return "absent";
  }
  return entryIsOwned(existing, serverPath) ? "owned" : "foreign";
}

export interface McpResult {
  ok: boolean;
  written: boolean;
  messages: string[];
  /** The host's own output, for the mechanisms that read its store back. */
  output?: string;
  /** True when the host's command interface could not be run at all. */
  unreachable?: boolean;
}

/**
 * Renders the manual registration line for a host with no usable interface.
 *
 * It is the exact command to paste, so its argv order is the command-line
 * mechanism's: `--env` pairs before the `--` terminator, because everything
 * after the terminator is handed to the server as arguments and an option
 * there is silently dropped.
 */
export function manualRegistrationLine(
  hostId: string,
  serverPath: string,
  environment: Record<string, string> = {},
  executable: string = hostId,
): string {
  const env: Record<string, string> = { ...environment, [MCP_HARNESS_ENV]: hostId };
  const flags: string = Object.entries(env)
    .map(([name, value]) => `--env ${name}=${value}`)
    .join(" ");
  return `${executable} mcp add --scope user ${flags} ${MCP_SERVER_NAME} -- ${MCP_ENTRY_MEMBERS.command} ${MCP_LAUNCHER_COMMAND} ${serverPath}`
    .replace(/\s+/g, " ")
    .trim();
}