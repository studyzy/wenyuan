import { describe, expect, it } from "vitest";
import {
  MCP_HARNESS_ENV,
  MCP_SERVER_NAME,
  MCP_SERVERS_KEY,
} from "../../src/mcp/mechanisms";
import { PER_USER_DIRECTORY_NAME } from "../../src/runtime/layout";
import { OWNERSHIP_MARKER } from "../../src/write/marker";
import { runInstaller, withFakeHome, type FakeHome, type JsonDocument } from "../helpers/fake-home";

/**
 * Per-host MCP registration driven through the real installer.
 *
 * Each approved host registers by its own mechanism: the command-line host
 * writes no file of ours and goes through its own interface, the TOML host
 * writes a table under its server-container key, and the JSON host writes a
 * server-map member resolved through its candidate list. In every case the
 * entry names the calling host and points at the staged server.
 */

const CLI_HOST = "claude-code";
const TOML_HOST = "codex";
const JSON_HOST = "codebuddy";
const TOML_PATH = ".codex/config.toml";
const JSON_PATH = ".codebuddy/.mcp.json";

/** Every non-runtime file whose text carries the package's marker. */
async function writtenFiles(home: FakeHome): Promise<string[]> {
  const found: string[] = [];
  for (const relative of await home.listFiles()) {
    if (relative.startsWith(PER_USER_DIRECTORY_NAME)) {
      continue;
    }
    if ((await home.read(relative)).includes(MCP_SERVER_NAME)) {
      found.push(relative);
    }
  }
  return found;
}

describe("the command-line host writes no config file of ours", () => {
  it("reports the manual line and still completes when the interface is absent", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const result = await runInstaller(["install", CLI_HOST]);
      expect(result.status).toBe(0);
      expect(result.combined()).toContain("mcp-server.mjs");
      expect(await home.exists(".claude.json")).toBe(false);
    });
  });

  it("writes no MCP config anywhere in the home", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", CLI_HOST])).status).toBe(0);
      const written = await writtenFiles(home);
      // Only the lifecycle-hook config carries the marker; no server-map entry
      // of ours is written for this host.
      for (const file of written) {
        expect(file).not.toContain("mcp.json");
        expect(file).not.toContain(".claude.json");
      }
    });
  });
});

describe("the TOML host writes a table under its server-container key", () => {
  it("writes one table with the launcher, argv and harness environment", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", TOML_HOST])).status).toBe(0);
      const text = await home.read(TOML_PATH);
      expect(text).toContain(`[mcp_servers.${MCP_SERVER_NAME}]`);
      expect(text).toContain(OWNERSHIP_MARKER);
      expect(text).toContain(MCP_HARNESS_ENV);
      expect(text).toContain(TOML_HOST);
    });
  });

  it("keeps an unrelated TOML section on a re-install", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      await home.write(TOML_PATH, 'model = "opus"\n');
      expect((await runInstaller(["install", TOML_HOST])).status).toBe(0);
      expect((await runInstaller(["install", TOML_HOST])).status).toBe(0);
      const text = await home.read(TOML_PATH);
      expect(text).toContain('model = "opus"');
      expect(text.match(new RegExp(`\\[mcp_servers\\.${MCP_SERVER_NAME}\\]`, "g"))).toHaveLength(1);
    });
  });
});

describe("the JSON host writes a server-map member", () => {
  it("writes into the highest-priority candidate when none exists", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", JSON_HOST])).status).toBe(0);
      const document: JsonDocument = await home.readJson(JSON_PATH);
      const servers = document[MCP_SERVERS_KEY] as JsonDocument;
      expect(Object.keys(servers)).toEqual([MCP_SERVER_NAME]);
      const entry = servers[MCP_SERVER_NAME] as JsonDocument;
      const env = entry["env"] as JsonDocument;
      expect(env[MCP_HARNESS_ENV]).toBe(JSON_HOST);
      const args = entry["args"] as string[];
      expect(args[0]).toContain(PER_USER_DIRECTORY_NAME);
      expect(args[0]).not.toContain(home.packageRoot);
    });
  });

  it("uninstall sweeps every candidate", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", JSON_HOST])).status).toBe(0);
      // A dormant duplicate in a lower-priority candidate than the one install
      // writes, carrying the same staged server path so it is recognized as
      // ours.
      const installed: JsonDocument = await home.readJson(JSON_PATH);
      const args: string[] = ((installed[MCP_SERVERS_KEY] as JsonDocument)[MCP_SERVER_NAME] as JsonDocument)[
        "args"
      ] as string[];
      await home.write(
        ".codebuddy/mcp.json",
        `${JSON.stringify({ mcpServers: { [MCP_SERVER_NAME]: { command: "node", args } } }, null, 2)}\n`,
      );
      expect((await runInstaller(["uninstall", JSON_HOST])).status).toBe(0);
      expect(await home.exists(JSON_PATH)).toBe(false);
      // The planted candidate held nothing but our entry, so the removal
      // emptied it and the file was deleted rather than left a husk.
      expect(await home.exists(".codebuddy/mcp.json")).toBe(false);
    });
  });
});

describe("every registration this package writes", () => {
  it.each([
    [TOML_HOST, TOML_PATH],
    [JSON_HOST, JSON_PATH],
  ])("names %s as the calling host in the MCP entry it writes", async (hostId: string, path: string) => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", hostId])).status).toBe(0);
      // Only the host's MCP entry file is asserted: the lifecycle-hook settings
      // file and its backup carry the marker but never name a host.
      expect(await home.read(path)).toContain(hostId);
    });
  });
});