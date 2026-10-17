import { MCP_HARNESS_ENV, MCP_LAUNCHER_COMMAND, MCP_ENTRY_MEMBERS } from "./mechanisms";

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

/**
 * True when an existing entry is this package's, judged by its exact shape:
 * the command is the node launcher and the argv names the packaged server entry
 * point. A name match alone does not confer ownership.
 */
export function entryIsOwned(value: unknown, serverPath: string): boolean {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const entry = value as Record<string, unknown>;
  if (entry[MCP_ENTRY_MEMBERS.command] !== MCP_LAUNCHER_COMMAND) {
    return false;
  }
  const args: unknown = entry[MCP_ENTRY_MEMBERS.args];
  if (!Array.isArray(args)) {
    return false;
  }
  return args.some((argument) => argument === serverPath);
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
}

/** Renders the manual registration line for a host with no usable interface. */
export function manualRegistrationLine(
  hostId: string,
  serverPath: string,
  environment: Record<string, string> = {},
): string {
  const env: Record<string, string> = { ...environment, [MCP_HARNESS_ENV]: hostId };
  const flags: string = Object.entries(env)
    .map(([name, value]) => `--env ${name}=${value}`)
    .join(" ");
  return `${hostId} mcp add ${MCP_ENTRY_MEMBERS.command} ${MCP_LAUNCHER_COMMAND} ${serverPath} ${flags}`.trim();
}