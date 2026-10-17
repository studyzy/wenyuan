import { describe, expect, it } from "vitest";
import {
  MCP_ENTRY_MEMBERS,
  MCP_HARNESS_ENV,
  MCP_LAUNCHER_COMMAND,
  MCP_SERVER_NAME,
  MCP_SERVERS_KEY,
  MCP_TOML_CONTAINER_KEY,
  MECHANISMS,
  containerKeyFor,
  declaredMechanismHosts,
  locationResolutionFor,
  mechanismFor,
  mechanismKindFor,
} from "../../src/mcp/mechanisms";
import { mcpServerEntry } from "../../src/mcp/ownership";
import { OWNERSHIP_MARKER } from "../../src/write/marker";

/**
 * The mechanism declaration: every approved host declares a mechanism and a
 * location resolution, the two file hosts declare the shared entry member
 * names, and the member names appear exactly once.
 */

const APPROVED_HOSTS = ["claude-code", "codex", "codebuddy"];
const FILE_HOSTS = ["codex", "codebuddy"];
const MECHANISM_KINDS = ["command-line", "toml-table", "json-server-map"];

describe("the per-host mechanism declaration", () => {
  it.each(APPROVED_HOSTS)("declares a mechanism for %s", (hostId: string) => {
    expect(declaredMechanismHosts()).toContain(hostId);
    expect(MECHANISM_KINDS).toContain(mechanismKindFor(hostId));
  });

  it("assigns each mechanism to exactly one host", () => {
    const kinds = MECHANISMS.map((declaration) => declaration.mechanism);
    expect(new Set(kinds).size).toBe(kinds.length);
  });

  it("gives the command-line host no config file of ours", () => {
    expect(mechanismKindFor("claude-code")).toBe("command-line");
    expect(containerKeyFor("claude-code")).toBeUndefined();
  });

  it("points the TOML host at one fixed path", () => {
    const resolution = locationResolutionFor("codex");
    expect(resolution?.kind).toBe("fixed");
  });

  it("gives the JSON host a priority-ordered candidate list", () => {
    const resolution = locationResolutionFor("codebuddy");
    expect(resolution?.kind).toBe("candidates");
    if (resolution?.kind === "candidates") {
      expect(resolution.paths.length).toBeGreaterThan(1);
    }
  });

  it("leaves an unapproved host with no declaration", () => {
    expect(mechanismFor("some-other-host")).toBeUndefined();
    expect(mechanismKindFor("some-other-host")).toBeUndefined();
  });
});

describe("the shared entry member names", () => {
  it("names the launcher, the argv and the environment exactly once", () => {
    expect(MCP_ENTRY_MEMBERS).toEqual({ command: "command", args: "args", environment: "env" });
  });

  it("spells the two file hosts' container keys verbatim", () => {
    expect(containerKeyFor("codex")).toBe(MCP_TOML_CONTAINER_KEY);
    expect(containerKeyFor("codebuddy")).toBe(MCP_SERVERS_KEY);
    expect(MCP_TOML_CONTAINER_KEY).toBe("mcp_servers");
    expect(MCP_SERVERS_KEY).toBe("mcpServers");
  });

  it("names the harness variable and the server name once", () => {
    expect(MCP_HARNESS_ENV).toBe("HINDSIGHT_MCP_HARNESS");
    expect(MCP_SERVER_NAME).toBe(OWNERSHIP_MARKER);
  });
});

describe("the shared entry shape", () => {
  it.each(FILE_HOSTS)("names the launcher, the argv and the harness for %s", (hostId: string) => {
    const staged = "/home/tester/.wenyuan/wenyuan-memory/mcp-server.mjs";
    const entry = mcpServerEntry({ serverPath: staged, hostId });
    expect(entry.command).toBe(MCP_LAUNCHER_COMMAND);
    expect(entry.args).toEqual([staged]);
    expect(entry.env?.[MCP_HARNESS_ENV]).toBe(hostId);
  });
});