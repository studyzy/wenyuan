import { describe, expect, it } from "vitest";
import { HOSTS } from "../../src/hosts/registry";
import type { HostContribution } from "../../src/hosts/types";
import {
  MCP_HARNESS_ENV,
  MCP_SERVERS_KEY,
  MCP_SERVER_NAME,
} from "../../src/mcp/register";
import { PER_USER_DIRECTORY_NAME } from "../../src/runtime/layout";
import { runInstaller, withFakeHome, type FakeHome, type JsonDocument } from "../helpers/fake-home";

/**
 * The ownership guard over the MCP server entry.
 *
 * A same-named entry this package did not create is never overwritten on
 * install and never removed on uninstall: the conflict is reported instead, and
 * a host blocked by it is not wired at all. Every registration this package
 * does write names its calling host through the caller-identifying variable,
 * because all hosts launch the same packaged server.
 */

interface Registration {
  file: string;
  entry: JsonDocument;
}

function foreignEntry(command: string): string {
  return `${JSON.stringify(
    { mcpServers: { [MCP_SERVER_NAME]: { command, args: ["/opt/someone-else.mjs"] } } },
    null,
    2,
  )}\n`;
}

async function registrations(home: FakeHome): Promise<Registration[]> {
  const found: Registration[] = [];
  for (const relative of await home.listFiles()) {
    // The staged runtime is skipped: only what was written into the host counts.
    if (relative.startsWith(PER_USER_DIRECTORY_NAME)) {
      continue;
    }
    const text: string = await home.read(relative);
    if (!text.includes(MCP_SERVER_NAME)) {
      continue;
    }
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch {
      continue;
    }
    if (parsed === null || typeof parsed !== "object") {
      continue;
    }
    const servers: unknown = (parsed as JsonDocument)[MCP_SERVERS_KEY];
    if (servers === null || typeof servers !== "object") {
      continue;
    }
    const entry: unknown = (servers as JsonDocument)[MCP_SERVER_NAME];
    if (entry !== null && typeof entry === "object") {
      found.push({ file: relative, entry: entry as JsonDocument });
    }
  }
  return found;
}

describe("a same-named MCP entry this package did not create", () => {
  it("is left byte-identical and the host is not wired", async () => {
    const planted: string = foreignEntry("python");
    await withFakeHome(
      { files: { ".claude.json": planted }, environment: { PATH: "" } },
      async (home: FakeHome) => {
        const result = await runInstaller(["install", "claude-code"]);
        expect(result.status).not.toBe(0);
        expect(result.combined()).toContain(MCP_SERVER_NAME);
        expect(await home.read(".claude.json")).toBe(planted);
        expect(await home.exists(".claude/settings.json")).toBe(false);
        expect(await home.exists(".claude/skills")).toBe(false);
      },
    );
  });

  it("survives an uninstall that removes everything else of ours", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
      const planted: string = foreignEntry("python");
      await home.write(".claude.json", planted);

      expect((await runInstaller(["uninstall", "claude-code"])).status).toBe(0);
      expect(await home.read(".claude.json")).toBe(planted);
      expect(await home.exists(".claude/settings.json")).toBe(false);
      expect(await home.exists(`.claude/skills/${MCP_SERVER_NAME}/claude-code/SKILL.md`)).toBe(false);
    });
  });
});

describe("every registration this package writes", () => {
  it.each([...HOSTS.map((host: HostContribution) => host.id)])(
    "names %s as the calling host",
    async (hostId: string) => {
      await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
        expect((await runInstaller(["install", hostId])).status).toBe(0);
        const written: Registration[] = await registrations(home);
        expect(written.length).toBeGreaterThan(0);
        for (const registration of written) {
          const env: unknown = registration.entry["env"];
          expect(env !== null && typeof env === "object", registration.file).toBe(true);
          expect((env as JsonDocument)[MCP_HARNESS_ENV]).toBe(hostId);
        }
      });
    },
  );

  it("points each registration at the staged server", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "codebuddy"])).status).toBe(0);
      const written: Registration[] = await registrations(home);
      expect(written).toHaveLength(1);
      const args: unknown = written[0]?.entry["args"];
      expect(Array.isArray(args)).toBe(true);
      for (const argument of args as string[]) {
        expect(argument).toContain(PER_USER_DIRECTORY_NAME);
        expect(argument).not.toContain(home.packageRoot);
      }
    });
  });
});
