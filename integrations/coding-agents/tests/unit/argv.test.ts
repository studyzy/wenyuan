import { describe, expect, it } from "vitest";
import { COMMANDS, isCommand, parseArgs, type ParsedArgs } from "../../src/cli/argv";

/**
 * Argument parsing: both spellings of every long flag, and a flag's value never
 * collected as a host name.
 */

interface FlagCase {
  name: string;
  argv: string[];
  command?: string;
  hosts: string[];
  server?: string;
  apiUrl?: string;
  apiToken?: string;
  importConversations?: boolean;
  enableWorkspaceMcp?: boolean;
}

const CASES: FlagCase[] = [
  {
    name: "a bare host name",
    argv: ["install", "claude-code"],
    command: "install",
    hosts: ["claude-code"],
  },
  {
    name: "a value in the space-separated spelling",
    argv: ["install", "--server", "local", "claude-code"],
    command: "install",
    hosts: ["claude-code"],
    server: "local",
  },
  {
    name: "a value in the equals-joined spelling",
    argv: ["install", "--server=local", "claude-code"],
    command: "install",
    hosts: ["claude-code"],
    server: "local",
  },
  {
    name: "a value that follows its hosts",
    argv: ["install", "claude-code", "--server", "local"],
    command: "install",
    hosts: ["claude-code"],
    server: "local",
  },
  {
    name: "a service address, which is never a host name",
    argv: ["install", "--api-url", "http://127.0.0.1:18888", "codex"],
    command: "install",
    hosts: ["codex"],
    apiUrl: "http://127.0.0.1:18888",
  },
  {
    name: "a token in the equals-joined spelling",
    argv: ["install", "--api-token=secret", "codebuddy"],
    command: "install",
    hosts: ["codebuddy"],
    apiToken: "secret",
  },
  {
    name: "several hosts at once",
    argv: ["install", "claude-code", "codex"],
    command: "install",
    hosts: ["claude-code", "codex"],
  },
  {
    name: "the all-hosts keyword",
    argv: ["uninstall", "all"],
    command: "uninstall",
    hosts: ["all"],
  },
  {
    name: "a host named before the command word's flags",
    argv: ["--import-conversations", "install", "claude-code"],
    command: "install",
    hosts: ["claude-code"],
    importConversations: true,
  },
  {
    name: "the workspace-MCP opt-in flag",
    argv: ["install", "claude-code", "--enable-workspace-mcp"],
    command: "install",
    hosts: ["claude-code"],
    enableWorkspaceMcp: true,
  },
  {
    name: "an unrecognized flag, which is dropped rather than collected",
    argv: ["install", "--nonsense", "claude-code"],
    command: "install",
    hosts: ["claude-code"],
  },
  {
    name: "no command word at all",
    argv: [],
    hosts: [],
  },
  {
    name: "flags with no command word",
    argv: ["--server", "local"],
    hosts: [],
    server: "local",
  },
];

describe("argument parsing", () => {
  it.each(CASES)("reads $name", (testCase: FlagCase) => {
    const parsed: ParsedArgs = parseArgs(testCase.argv);
    expect(parsed.command).toBe(testCase.command);
    expect(parsed.hosts).toEqual(testCase.hosts);
    expect(parsed.flags.server).toBe(testCase.server);
    expect(parsed.flags.apiUrl).toBe(testCase.apiUrl);
    expect(parsed.flags.apiToken).toBe(testCase.apiToken);
    expect(parsed.flags.importConversations).toBe(testCase.importConversations ?? false);
    expect(parsed.flags.enableWorkspaceMcp).toBe(testCase.enableWorkspaceMcp ?? false);
  });

  it("treats the first bare argument as the command word whatever it says", () => {
    expect(parseArgs(["bogus", "claude-code"]).command).toBe("bogus");
    expect(parseArgs(["bogus", "claude-code"]).hosts).toEqual(["claude-code"]);
  });

  it("reads a command that takes no host at all", () => {
    expect(parseArgs(["stats"]).hosts).toEqual([]);
    expect(parseArgs(["update"]).hosts).toEqual([]);
  });
});

describe("the command words", () => {
  it.each([...COMMANDS])("accepts %s", (word: string) => {
    expect(isCommand(word)).toBe(true);
  });

  it.each(["list", "Install", "", "all"])("rejects %s", (word: string) => {
    expect(isCommand(word)).toBe(false);
  });
});
