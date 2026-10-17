import { describe, expect, it } from "vitest";
import { HOSTS } from "../../src/hosts/registry";
import type { HostContribution } from "../../src/hosts/types";
import { MCP_SERVERS_KEY, MCP_SERVER_NAME } from "../../src/mcp/mechanisms";
import { runtimeDirectory } from "../../src/runtime/layout";
import { OWNERSHIP_MARKER } from "../../src/write/marker";
import { declaredEvents, eventCommands, HOOKS_CONTAINER_KEY, ownEntryCount } from "../helpers/hooks";
import { runInstaller, withFakeHome, type FakeHome, type JsonDocument } from "../helpers/fake-home";

/**
 * Where each host's declared locations actually are.
 *
 * The paths are spelled out here rather than read back out of the registry, so
 * a host module that moves its config, MCP or skills location fails this suite
 * instead of silently redefining what the installer writes. The command-line
 * host keeps its MCP registration in its own store and declares no MCP config
 * file of ours, so its entry is asserted through its own suite instead.
 */

interface ExpectedLocations {
  id: string;
  config: string;
  skills: string;
}

const LOCATIONS: ExpectedLocations[] = [
  { id: "claude-code", config: ".claude/settings.json", skills: ".claude/skills" },
  { id: "codex", config: ".codex/settings.json", skills: ".codex/skills" },
  { id: "codebuddy", config: ".codebuddy/settings.json", skills: ".codebuddy/skills" },
];

function locationsFor(hostId: string): ExpectedLocations {
  const found: ExpectedLocations | undefined = LOCATIONS.find((entry) => entry.id === hostId);
  if (!found) {
    throw new Error(`no expected locations declared for ${hostId}`);
  }
  return found;
}

function ownCommands(commands: readonly string[]): string[] {
  return commands.filter((command) => command.includes(OWNERSHIP_MARKER));
}

describe("installing into a home where the host has no config at all", () => {
  it.each([...HOSTS.map((host: HostContribution) => host.id)])("wires %s", async (hostId: string) => {
    const expected: ExpectedLocations = locationsFor(hostId);
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const result = await runInstaller(["install", hostId]);
      expect(result.status).toBe(0);

      const staged: string = runtimeDirectory();
      expect(await home.isFile(expected.config)).toBe(true);
      const document: JsonDocument = await home.readJson(expected.config);
      for (const event of declaredEvents(hostId)) {
        expect(ownEntryCount(document, event), `${hostId} ${event}`).toBe(1);
        const commands: string[] = ownCommands(eventCommands(document, event));
        expect(commands, `${hostId} ${event}`).toHaveLength(1);
        expect(commands[0]).toContain(staged);
      }

      expect(await home.isFile(`${expected.skills}/${OWNERSHIP_MARKER}/${hostId}/SKILL.md`)).toBe(true);
    });
  });

  it("registers the JSON host's entry under the server-map key", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "codebuddy"])).status).toBe(0);
      const mcp: JsonDocument = await home.readJson(".codebuddy/.mcp.json");
      const servers: unknown = mcp[MCP_SERVERS_KEY];
      expect(servers !== null && typeof servers === "object").toBe(true);
      expect(Object.keys(servers as JsonDocument)).toEqual([MCP_SERVER_NAME]);
    });
  });

  it("registers the TOML host's entry under the server-container key", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "codex"])).status).toBe(0);
      expect(await home.read(".codex/config.toml")).toContain(`[mcp_servers.${MCP_SERVER_NAME}]`);
    });
  });

  it("writes no MCP config file for the command-line host", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
      expect(await home.exists(".claude.json")).toBe(false);
    });
  });

  it("registers exactly the three approved lifecycle events and no others", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
      const hooks: unknown = (await home.readJson(".claude/settings.json"))[HOOKS_CONTAINER_KEY];
      expect(Object.keys(hooks as JsonDocument).sort()).toEqual(declaredEvents("claude-code").sort());
    });
  });

  it("creates the parent directories a host config lives in", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect(await home.exists(".claude")).toBe(false);
      expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
      expect(await home.exists(".claude")).toBe(true);
      expect(await home.exists(".claude/skills")).toBe(true);
    });
  });
});
