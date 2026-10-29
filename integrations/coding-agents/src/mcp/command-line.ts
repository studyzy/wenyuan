import { spawn } from "node:child_process";
import {
  MCP_ENTRY_MEMBERS,
  MCP_HARNESS_ENV,
  MCP_LAUNCHER_COMMAND,
  MCP_SERVER_NAME,
} from "./mechanisms";
import { OWNERSHIP_MARKER } from "../write/marker";
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

/**
 * The argv the host's add command receives, with no shell involved.
 *
 * The option order is load-bearing: `--` ends the host's own option parsing, so
 * everything after it is the server's argv. The `--env` pairs therefore come
 * BEFORE the terminator — written after it they are handed to the server as
 * arguments, and the entry is registered with no environment at all. That is
 * exactly the failure that leaves the server unable to tell which harness
 * launched it.
 */
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
    ...flags,
    MCP_SERVER_NAME,
    "--",
    MCP_ENTRY_MEMBERS.command,
    MCP_LAUNCHER_COMMAND,
    input.serverPath,
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
 * True when the host's own entry for this name is one this package wrote: a
 * node launcher whose first argv element is the packaged server. A foreign
 * entry under the same name — an operator's own server — is never removed.
 */
export function isOurCommandLine(args: readonly unknown[]): boolean {
  // The listing's own argv, or the argv right of a `--` terminator when the
  // host prints the add command it would run. Both carry `node` first.
  const separator: number = args.indexOf("--");
  const serverArgs: readonly unknown[] = separator === -1 ? args : args.slice(separator + 1);
  const launcher: unknown = serverArgs[0];
  const script: unknown = serverArgs[1];
  return (
    launcher === MCP_LAUNCHER_COMMAND &&
    typeof script === "string" &&
    script.split(/[\\/]/).includes(OWNERSHIP_MARKER) &&
    script.split(/[\\/]/).pop() === "mcp-server.mjs"
  );
}

/**
 * Registers the entry: removes any entry of ours under the name, then adds it,
 * so a re-install repoints the server instead of failing on a name the host
 * already holds. A refused add is a failure to report, not a silent success —
 * a host that ends up with no entry has no memory tools, and the operator has
 * to know.
 */
export async function registerCommandLine(input: CommandLineInput): Promise<McpResult> {
  const removed: McpResult = await removeOurEntry(input);
  if (!removed.ok) {
    return fallback(input);
  }
  const listed: McpResult = await invoke(input.executable, listArguments());
  if (listed.ok && !isOurCommandLine(parseServerArgs(listed.output ?? ""))) {
    // A foreign entry holds the name. Abort before the add, which would fail
    // anyway, and say why.
    return {
      ok: false,
      written: false,
      messages: [
        `${input.executable} already has an MCP server named "${MCP_SERVER_NAME}" that this package did not write; ` +
          `leaving it alone. Remove it or rename it, then re-run install.`,
      ],
    };
  }
  const added: McpResult = await invoke(input.executable, addArguments(input));
  if (!added.ok) {
    return fallback(input);
  }
  return { ok: true, written: true, messages: [] };
}

/** Removes this package's entry, leaving a foreign same-named entry untouched. */
export async function removeCommandLine(input: CommandLineInput): Promise<McpResult> {
  const removed: McpResult = await removeOurEntry(input);
  if (!removed.ok) {
    // A failed removal is not a failure of the uninstall: the host's store is
    // the host's own, so the manual line is offered instead.
    return fallback(input);
  }
  return { ok: true, written: true, messages: [] };
}

/**
 * Removes the entry only when it is ours. The host keeps its entry in its own
 * store, so this asks the host what it holds rather than guessing; an entry
 * that cannot be listed, or one that is not ours, is left untouched.
 */
async function removeOurEntry(input: CommandLineInput): Promise<McpResult> {
  const listed: McpResult = await invoke(input.executable, listArguments());
  if (!listed.ok) {
    // A name the host does not hold is not a failure — there is nothing of ours
    // to remove — but a listing that could not be RUN at all means the host's
    // interface is unusable, and every later call would fail the same way.
    return listed.unreachable === true
      ? listed
      : { ok: true, written: false, messages: [] };
  }
  if (!isOurCommandLine(parseServerArgs(listed.output ?? ""))) {
    return { ok: true, written: false, messages: [] };
  }
  return invoke(input.executable, removeArguments(input));
}

/** The argv that asks the host for one server's definition. */
function listArguments(): string[] {
  return ["mcp", "get", MCP_SERVER_NAME];
}

/**
 * The server argv inside the host's listing output.
 *
 * The host prints a human-readable block rather than JSON, so the reader takes
 * the first `node` token and then the FIRST script-looking token after it,
 * skipping the block's own labels (`Args:`, `Command:`) — a label sits between
 * the launcher and the script in every rendering of it.
 */
export function parseServerArgs(output: string): string[] {
  const tokens: string[] = output.split(/\s+/).filter((token) => token.length > 0);
  const index: number = tokens.indexOf(MCP_LAUNCHER_COMMAND);
  if (index === -1) {
    return [];
  }
  const script: string | undefined = tokens
    .slice(index + 1)
    .find((token) => token.includes("/") || token.endsWith(".js") || token.endsWith(".mjs"));
  return script === undefined ? [] : [MCP_LAUNCHER_COMMAND, script];
}

function fallback(input: CommandLineInput): McpResult {
  return {
    ok: false,
    written: false,
    messages: [
      `could not reach the ${input.executable} command interface, so the memory tools were not registered; ` +
        `run this by hand and re-run install:`,
      manualRegistrationLine(input.hostId, input.serverPath, input.environment, input.executable),
    ],
    output: "the command interface could not be run",
  };
}

/** Spawns the host command with an argv array, never a shell string. */
function invoke(executable: string, args: readonly string[]): Promise<McpResult> {
  return new Promise((resolve) => {
    let settled = false;
    let output = "";
    const finish = (result: McpResult): void => {
      if (!settled) {
        settled = true;
        resolve(result);
      }
    };
    let child;
    try {
      // The output is captured, not discarded: "what does this name already
      // hold" is answered from the host's own listing, and a discarding spawn
      // could only guess.
      child = spawn(executable, [...args], { stdio: ["ignore", "pipe", "pipe"], shell: false });
    } catch {
      finish({ ok: false, written: false, messages: [], unreachable: true });
      return;
    }
    child.stdout?.on("data", (chunk: Buffer) => {
      output += chunk.toString();
    });
    child.stderr?.on("data", (chunk: Buffer) => {
      output += chunk.toString();
    });
    child.on("error", () => finish({ ok: false, written: false, messages: [], unreachable: true }));
    child.on("close", (code: number | null) =>
      finish({ ok: code === 0, written: false, messages: [], output }),
    );
  });
}