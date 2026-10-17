import { spawn } from "node:child_process";
import {
  MCP_ENTRY_MEMBERS,
  MCP_HARNESS_ENV,
  MCP_LAUNCHER_COMMAND,
  MCP_SERVER_NAME,
} from "./mechanisms";
import { manualRegistrationLine, type McpEntryInput, type McpResult } from "./ownership";

/**
 * The command-line host's registration mechanism.
 *
 * This host keeps no file of ours: its server entry is added through the host's
 * own command interface at user scope, under the package's server name, with
 * the harness-attribution variable passed as an environment flag of the add
 * command. Because the host refuses a name it already holds, install removes
 * the same-named entry first and then adds it, so a re-install repoints the
 * server rather than failing. When the interface is absent or refuses, the
 * manual registration line is reported and the invocation completes without a
 * failure escaping and without writing any file of ours.
 */

export interface CommandLineInput extends McpEntryInput {
  /** The host's own executable, for example the operator-facing host name. */
  executable: string;
  /** Overrides the subcommand spelling; defaults to the add/remove pair. */
  subcommands?: { add: readonly string[]; remove: readonly string[] };
}

const DEFAULT_SUBCOMMANDS = { add: ["mcp", "add"], remove: ["mcp", "remove"] } as const;

/** The argv the host's add command receives, with no shell involved. */
export function addArguments(input: CommandLineInput): string[] {
  const sub: readonly string[] = input.subcommands?.add ?? DEFAULT_SUBCOMMANDS.add;
  const envPairs: string[] = Object.entries(environmentOf(input)).map(
    ([name, value]) => `${name}=${value}`,
  );
  const flags: string[] = envPairs.flatMap((pair) => ["--env", pair]);
  return [
    ...sub,
    "--scope",
    "user",
    MCP_SERVER_NAME,
    "--",
    MCP_ENTRY_MEMBERS.command,
    MCP_LAUNCHER_COMMAND,
    input.serverPath,
    ...flags,
  ];
}

/** The argv the host's remove command receives. */
export function removeArguments(input: CommandLineInput): string[] {
  const sub: readonly string[] = input.subcommands?.remove ?? DEFAULT_SUBCOMMANDS.remove;
  return [...sub, "--scope", "user", MCP_SERVER_NAME];
}

function environmentOf(input: McpEntryInput): Record<string, string> {
  return { ...(input.environment ?? {}), [MCP_HARNESS_ENV]: input.hostId };
}

/**
 * Registers the entry: removes the same-named entry then adds it, so the add
 * lands on a free name. Falls back to the manual line when the interface is
 * unusable, completing without a failure escaping.
 */
export async function registerCommandLine(input: CommandLineInput): Promise<McpResult> {
  const removed: McpResult = await invoke(input.executable, removeArguments(input));
  if (!removed.ok) {
    return fallback(input);
  }
  const added: McpResult = await invoke(input.executable, addArguments(input));
  if (!added.ok) {
    return fallback(input);
  }
  return { ok: true, written: true, messages: [] };
}

/** Removes this package's entry, leaving a foreign same-named entry untouched. */
export async function removeCommandLine(input: CommandLineInput): Promise<McpResult> {
  const removed: McpResult = await invoke(input.executable, removeArguments(input));
  if (!removed.ok) {
    // A failed removal is not a failure of the uninstall: the host's store is
    // the host's own, so the manual line is offered instead.
    return fallback(input);
  }
  return { ok: true, written: true, messages: [] };
}

function fallback(input: CommandLineInput): McpResult {
  return {
    ok: true,
    written: false,
    messages: [
      `could not reach the ${input.executable} command interface; run this by hand to register the same entry:`,
      manualRegistrationLine(input.hostId, input.serverPath, input.environment),
    ],
  };
}

/** Spawns the host command with an argv array, never a shell string. */
function invoke(executable: string, args: readonly string[]): Promise<McpResult> {
  return new Promise((resolve) => {
    let settled = false;
    const finish = (result: McpResult): void => {
      if (!settled) {
        settled = true;
        resolve(result);
      }
    };
    let child;
    try {
      child = spawn(executable, [...args], { stdio: "ignore", shell: false });
    } catch {
      finish({ ok: false, written: false, messages: [] });
      return;
    }
    child.on("error", () => finish({ ok: false, written: false, messages: [] }));
    child.on("close", (code: number | null) =>
      finish({ ok: code === 0, written: false, messages: [] }),
    );
  });
}