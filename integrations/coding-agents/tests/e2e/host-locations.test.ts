import { describe, expect, it } from "vitest";
import { HOSTS } from "../../src/hosts/registry";
import type { HostContribution } from "../../src/hosts/types";
import { LIFECYCLE_EVENTS } from "../../src/hosts/hooks";
import { MCP_SERVERS_KEY } from "../../src/mcp/register";
import { runtimeDirectory } from "../../src/runtime/layout";
import { OWNERSHIP_MARKER } from "../../src/write/marker";
import { runInstaller, withFakeHome, type FakeHome, type JsonDocument } from "../helpers/fake-home";

/**
 * Where each host's three declared locations actually are.
 *
 * The paths are spelled out here rather than read back out of the registry, so
 * a host module that moves its config, MCP or skills location fails this suite
 * instead of silently redefining what the installer writes.
 */

interface ExpectedLocations {
  id: string;
  config: string;
  mcp: string;
  skills: string;
}

const LOCATIONS: ExpectedLocations[] = [
  { id: "claude-code", config: ".claude/settings.json", mcp: ".claude.json", skills: ".claude/skills" },
  { id: "codex", config: ".codex/settings.json", mcp: ".codex/mcp.json", skills: ".codex/skills" },
  {
    id: "codebuddy",
    config: ".codebuddy/settings.json",
    mcp: ".codebuddy/mcp.json",
    skills: ".codebuddy/skills",
  },
];

function locationsFor(hostId: string): ExpectedLocations {
  const found: ExpectedLocations | undefined = LOCATIONS.find((entry) => entry.id === hostId);
  if (!found) {
    throw new Error(`no expected locations declared for ${hostId}`);
  }
  return found;
}

function eventEntries(document: JsonDocument, eventId: string): unknown[] {
  const hooks: unknown = document["hooks"];
  if (hooks === null || typeof hooks !== "object") {
    return [];
  }
  const entries: unknown = (hooks as JsonDocument)[eventId];
  return Array.isArray(entries) ? entries : [];
}

function ownCommands(entries: readonly unknown[]): string[] {
  return entries
    .map((entry) => (entry !== null && typeof entry === "object" ? (entry as JsonDocument)["command"] : undefined))
    .filter((command): command is string => typeof command === "string" && command.includes(OWNERSHIP_MARKER));
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
      for (const event of LIFECYCLE_EVENTS) {
        const entries: unknown[] = eventEntries(document, event.id);
        expect(entries, `${hostId} ${event.id}`).toHaveLength(1);
        const commands: string[] = ownCommands(entries);
        expect(commands, `${hostId} ${event.id}`).toHaveLength(1);
        expect(commands[0]).toContain(staged);
      }

      const mcp: JsonDocument = await home.readJson(expected.mcp);
      const servers: unknown = mcp[MCP_SERVERS_KEY];
      expect(servers !== null && typeof servers === "object").toBe(true);
      expect(Object.keys(servers as JsonDocument)).toEqual([OWNERSHIP_MARKER]);

      expect(await home.isFile(`${expected.skills}/${OWNERSHIP_MARKER}/${hostId}/SKILL.md`)).toBe(true);
    });
  });

  it("registers exactly the three approved lifecycle events and no others", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
      const hooks: unknown = (await home.readJson(".claude/settings.json"))["hooks"];
      expect(Object.keys(hooks as JsonDocument).sort()).toEqual(
        LIFECYCLE_EVENTS.map((event) => event.id).sort(),
      );
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
