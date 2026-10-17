import { join } from "node:path";
import type { ConfigFormat } from "../write/json-merge";
import {
  MCP_HARNESS_ENV,
  MCP_SERVER_NAME,
  MCP_SERVERS_KEY,
  mechanismKindFor,
} from "./mechanisms";
import {
  classifyEntry,
  mcpServerEntry as buildEntry,
  type McpEntryInput,
  type McpOwnership,
  type McpResult,
  type McpServerEntry,
} from "./ownership";
import {
  candidatePaths,
  registerJsonServerMap,
  removeJsonServerMap,
} from "./json-server-map";
import { registerTomlTable, removeTomlTable } from "./toml-table";
import { registerCommandLine, removeCommandLine } from "./command-line";

/**
 * The MCP registration entry point every host adapter calls.
 *
 * The mechanism is read from the per-host declaration rather than branched on
 * the host identifier, so a fourth host is added by adding one declaration
 * entry and one adapter file. The ownership decision, the one-time sibling
 * backup and the idempotent re-run guarantees are shared across every mechanism
 * so install, re-install and uninstall agree.
 */

export { MCP_HARNESS_ENV, MCP_SERVER_NAME, MCP_SERVERS_KEY };
export type { McpOwnership, McpResult, McpServerEntry, ConfigFormat };

export interface RegisterInput {
  /** The approved host whose MCP server is being registered. */
  hostId: string;
  /** The host's home directory; file locations resolve relative to it. */
  home: string;
  /** The absolute staged path of the packaged server entry point. */
  serverPath: string;
  /** Deployment-contract values the packaged runtime reads at launch. */
  environment?: Record<string, string>;
  /**
   * The host's own executable, for the command-line mechanism. When omitted the
   * host identifier is used, matching the operator-facing name.
   */
  executable?: string;
}

/** The stdio entry, naming the calling host in its environment. */
export function mcpServerEntry(input: McpEntryInput): McpServerEntry {
  return buildEntry(input);
}

function entryInput(input: RegisterInput): McpEntryInput {
  return {
    hostId: input.hostId,
    serverPath: input.serverPath,
    environment: input.environment,
  };
}

/**
 * What is already registered for this host, so an install can refuse a foreign
 * entry before it writes anything. The command-line host keeps its entry in the
 * host's own store, which this package does not read, so it reports "absent".
 */
export async function inspectMcpServer(input: RegisterInput): Promise<McpOwnership> {
  const kind: string | undefined = mechanismKindFor(input.hostId);
  if (kind !== "json-server-map") {
    return "absent";
  }
  const { existsSync, readFileSync } = await import("node:fs");
  for (const candidate of candidatePaths(input.hostId, input.home)) {
    if (!existsSync(candidate)) {
      continue;
    }
    let parsed: unknown;
    try {
      parsed = JSON.parse(readFileSync(candidate, "utf8"));
    } catch {
      continue;
    }
    const servers: unknown =
      parsed !== null && typeof parsed === "object" && !Array.isArray(parsed)
        ? (parsed as Record<string, unknown>)[MCP_SERVERS_KEY]
        : undefined;
    const existing =
      servers !== null && typeof servers === "object" && !Array.isArray(servers)
        ? (servers as Record<string, unknown>)[MCP_SERVER_NAME]
        : undefined;
    if (existing !== undefined) {
      return classifyEntry(existing, input.serverPath);
    }
  }
  return "absent";
}

/** Writes the registration through the declared mechanism for the host. */
export async function registerMcpServer(input: RegisterInput): Promise<McpResult> {
  const kind: string | undefined = mechanismKindFor(input.hostId);
  switch (kind) {
    case "json-server-map":
      return registerJsonServerMap({ ...entryInput(input), home: input.home });
    case "toml-table":
      return registerTomlTable({ ...entryInput(input), home: input.home });
    case "command-line":
      return registerCommandLine({
        ...entryInput(input),
        executable: input.executable ?? input.hostId,
      });
    default:
      return {
        ok: false,
        written: false,
        messages: [`no MCP registration mechanism is declared for ${input.hostId}.`],
      };
  }
}

/** Removes the registration through the declared mechanism for the host. */
export async function removeMcpServer(input: RegisterInput): Promise<McpResult> {
  const kind: string | undefined = mechanismKindFor(input.hostId);
  switch (kind) {
    case "json-server-map":
      return removeJsonServerMap({ ...entryInput(input), home: input.home });
    case "toml-table":
      return removeTomlTable({ ...entryInput(input), home: input.home });
    case "command-line":
      return removeCommandLine({
        ...entryInput(input),
        executable: input.executable ?? input.hostId,
      });
    default:
      return { ok: true, written: false, messages: [] };
  }
}

/** The staged server path a host's entry points at, given a runtime directory. */
export function stagedServerPath(runtimeDirectory: string, serverFileName: string): string {
  return join(runtimeDirectory, serverFileName);
}