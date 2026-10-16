/**
 * Argument parsing for the four commands and five flags.
 *
 * Two rules matter and both are load-bearing. A long flag is accepted in the
 * space-separated and the equals-joined spelling. And a flag's value is never
 * collected as a host name: a mode word or a service address travels as a bare
 * argument right after its flag, so a value-unaware parser would reject it as
 * an unknown host.
 */

export const COMMANDS = ["install", "uninstall", "update", "stats"] as const;

export type CommandName = (typeof COMMANDS)[number];

type ValueFlagKey = "server" | "apiUrl" | "apiToken";
type BooleanFlagKey = "importConversations" | "enableWorkspaceMcp";

export interface CommandFlags {
  server?: string;
  apiUrl?: string;
  apiToken?: string;
  importConversations: boolean;
  enableWorkspaceMcp: boolean;
}

export interface ParsedArgs {
  /** The first bare argument, whatever it says; the dispatcher validates it. */
  command?: string;
  flags: CommandFlags;
  /** Bare arguments after the command word: host names or the all keyword. */
  hosts: string[];
}

/** The flags that take a value, in both spellings. */
export const VALUE_FLAGS: Record<string, ValueFlagKey> = {
  "--server": "server",
  "--api-url": "apiUrl",
  "--api-token": "apiToken",
};

/** The opt-in flags, which are true when they are present at all. */
export const BOOLEAN_FLAGS: Record<string, BooleanFlagKey> = {
  "--import-conversations": "importConversations",
  "--enable-workspace-mcp": "enableWorkspaceMcp",
};

export function isCommand(value: string): value is CommandName {
  return (COMMANDS as readonly string[]).includes(value);
}

export function parseArgs(argv: readonly string[]): ParsedArgs {
  const flags: CommandFlags = { importConversations: false, enableWorkspaceMcp: false };
  const hosts: string[] = [];
  let command: string | undefined;

  for (let index = 0; index < argv.length; index += 1) {
    const argument: string | undefined = argv[index];
    if (argument === undefined || argument.length === 0) {
      continue;
    }
    if (argument.startsWith("--")) {
      const separator: number = argument.indexOf("=");
      const name: string = separator === -1 ? argument : argument.slice(0, separator);
      const inline: string | undefined = separator === -1 ? undefined : argument.slice(separator + 1);

      const valueKey: ValueFlagKey | undefined = VALUE_FLAGS[name];
      if (valueKey !== undefined) {
        if (inline !== undefined) {
          flags[valueKey] = inline;
          continue;
        }
        const next: string | undefined = argv[index + 1];
        index += 1;
        if (next !== undefined) {
          flags[valueKey] = next;
        }
        continue;
      }

      const booleanKey: BooleanFlagKey | undefined = BOOLEAN_FLAGS[name];
      if (booleanKey !== undefined) {
        flags[booleanKey] = true;
      }
      // Anything else that looks like a flag is dropped rather than collected
      // as a host name, so an unexpected flag cannot fail host validation.
      continue;
    }
    if (command === undefined) {
      command = argument;
      continue;
    }
    hosts.push(argument);
  }

  return { command, flags, hosts };
}
