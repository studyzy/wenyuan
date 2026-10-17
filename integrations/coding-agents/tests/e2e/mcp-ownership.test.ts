import { describe, expect, it } from "vitest";
import {
  MCP_SERVERS_KEY,
  MCP_SERVER_NAME,
} from "../../src/mcp/mechanisms";
import { PER_USER_DIRECTORY_NAME } from "../../src/runtime/layout";
import { runInstaller, withFakeHome, type FakeHome, type JsonDocument } from "../helpers/fake-home";

/**
 * The ownership guard over the MCP server entry.
 *
 * A same-named entry this package did not create is never overwritten on
 * install: the conflict is reported instead, and the host is not wired. The
 * guard applies to a host whose registration is a file of ours (the JSON and
 * TOML hosts); the command-line host's store is its own and this package never
 * writes a conflicting file. Every entry this package does write names its
 * calling host through the caller-identifying variable.
 */

const JSON_HOST = "codebuddy";
const JSON_PATH = ".codebuddy/.mcp.json";

function foreignEntry(): string {
  return `${JSON.stringify(
    { mcpServers: { [MCP_SERVER_NAME]: { command: "python", args: ["/opt/someone-else.mjs"] } } },
    null,
    2,
  )}\n`;
}

async function registrations(home: FakeHome): Promise<{ file: string; entry: JsonDocument }[]> {
  const found: { file: string; entry: JsonDocument }[] = [];
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
    const planted: string = foreignEntry();
    await withFakeHome(
      { files: { [JSON_PATH]: planted }, environment: { PATH: "" } },
      async (home: FakeHome) => {
        const result = await runInstaller(["install", JSON_HOST]);
        expect(result.status).not.toBe(0);
        expect(result.combined()).toContain(MCP_SERVER_NAME);
        expect(await home.read(JSON_PATH)).toBe(planted);
        expect(await home.exists(".codebuddy/skills")).toBe(false);
      },
    );
  });

  it("survives an uninstall that removes everything else of ours", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", JSON_HOST])).status).toBe(0);
      // Replace our entry with a foreign one of the same name, then uninstall.
      const planted: string = foreignEntry();
      await home.write(JSON_PATH, planted);

      expect((await runInstaller(["uninstall", JSON_HOST])).status).toBe(0);
      expect(await home.read(JSON_PATH)).toBe(planted);
    });
  });
});

describe("every registration this package writes", () => {
  it.each([
    ["codex", ".codex/config.toml"],
    ["codebuddy", JSON_PATH],
  ])(
    "names %s as the calling host in the MCP entry it wrote",
    async (hostId: string, path: string) => {
      // Only the host's MCP entry file is asserted: the lifecycle-hook settings
      // file and its backup carry the marker but never name a host.
      await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
        expect((await runInstaller(["install", hostId])).status).toBe(0);
        expect(await home.read(path)).toContain(hostId);
      });
    },
  );

  it("points the JSON host's registration at the staged server", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", JSON_HOST])).status).toBe(0);
      const written = await registrations(home);
      expect(written).toHaveLength(1);
      const args: unknown = written[0]?.entry["args"];
      expect(Array.isArray(args)).toBe(true);
      for (const argument of args as string[]) {
        expect(argument).toContain(PER_USER_DIRECTORY_NAME);
        expect(argument).not.toContain(home.packageRoot);
      }
    });
  });

  it("points the TOML host's registration at the staged server", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "codex"])).status).toBe(0);
      const text = await home.read(".codex/config.toml");
      expect(text).toContain(PER_USER_DIRECTORY_NAME);
      expect(text).not.toContain(home.packageRoot);
    });
  });
});