import { describe, expect, it } from "vitest";
import {
  inspectMcpServer,
  mcpServerEntry,
  registerMcpServer,
  removeMcpServer,
} from "../../src/mcp/register";
import {
  MCP_HARNESS_ENV,
  MCP_SERVER_NAME,
  MCP_SERVERS_KEY,
} from "../../src/mcp/mechanisms";
import { OWNERSHIP_MARKER } from "../../src/write/marker";
import { withFakeHome, type FakeHome, type JsonDocument } from "../helpers/fake-home";

/**
 * The dispatch router: it selects the declared mechanism per host, keeps one
 * ownership/backup/idempotency policy across every mechanism, and preserves the
 * foundation's uniform JSON server-map as the JSON host's behavior.
 */

const staged = `/home/tester/.wenyuan/${OWNERSHIP_MARKER}/mcp-server.mjs`;
const JSON_HOST = "codebuddy";
const TOML_HOST = "codex";
const CLI_HOST = "claude-code";
const JSON_PATH = ".codebuddy/.mcp.json";

function serversOf(document: JsonDocument): JsonDocument {
  const servers: unknown = document[MCP_SERVERS_KEY];
  return servers !== null && typeof servers === "object" && !Array.isArray(servers)
    ? (servers as JsonDocument)
    : {};
}

describe("the shared entry shape", () => {
  it("names the calling host and points at the staged server", () => {
    const entry = mcpServerEntry({ serverPath: staged, hostId: JSON_HOST });
    expect(entry.env?.[MCP_HARNESS_ENV]).toBe(JSON_HOST);
    expect(entry.args).toContain(staged);
    expect(JSON.stringify(entry)).toContain(OWNERSHIP_MARKER);
  });
});

describe("dispatching to the declared mechanism", () => {
  it("routes the JSON host to the server-map mechanism", async () => {
    await withFakeHome({}, async (home: FakeHome) => {
      const result = await registerMcpServer({ hostId: JSON_HOST, home: home.home, serverPath: staged });
      expect(result.ok).toBe(true);
      const document = await home.readJson(JSON_PATH);
      expect(Object.keys(serversOf(document))).toEqual([MCP_SERVER_NAME]);
    });
  });

  it("routes the TOML host to the table mechanism", async () => {
    await withFakeHome({}, async (home: FakeHome) => {
      const result = await registerMcpServer({ hostId: TOML_HOST, home: home.home, serverPath: staged });
      expect(result.ok).toBe(true);
      expect(await home.read(".codex/config.toml")).toContain("[mcp_servers.");
    });
  });

  it("routes the command-line host with no config file of ours", async () => {
    await withFakeHome({}, async (home: FakeHome) => {
      const result = await registerMcpServer({
        hostId: CLI_HOST,
        home: home.home,
        serverPath: staged,
        executable: `${home.home}/bin/does-not-exist`,
      });
      // The interface is unusable, so the manual line is reported, no config
      // file is written, and the run does not claim a registration it never
      // made: a host with no MCP entry has no memory tools.
      expect(result.ok).toBe(false);
      expect(result.written).toBe(false);
      expect(await home.isFile(".claude.json")).toBe(false);
      expect(result.messages.join("\n")).toContain(staged);
    });
  });

  it("refuses an unapproved host", async () => {
    await withFakeHome({}, async (home: FakeHome) => {
      const result = await registerMcpServer({ hostId: "nope", home: home.home, serverPath: staged });
      expect(result.ok).toBe(false);
    });
  });
});

describe("the ownership guard on the JSON host", () => {
  it("reports a conflict and writes nothing when the name is foreign", async () => {
    const before = `${JSON.stringify({ [MCP_SERVERS_KEY]: { [MCP_SERVER_NAME]: { command: "someone" } } }, null, 2)}\n`;
    await withFakeHome({ files: { [JSON_PATH]: before } }, async (home: FakeHome) => {
      const result = await registerMcpServer({ hostId: JSON_HOST, home: home.home, serverPath: staged });
      expect(result.ok).toBe(false);
      expect(result.written).toBe(false);
      expect(result.messages.join(" ")).toContain(MCP_SERVER_NAME);
      expect(await home.read(JSON_PATH)).toBe(before);
      expect(await inspectMcpServer({ hostId: JSON_HOST, home: home.home, serverPath: staged })).toBe(
        "foreign",
      );
    });
  });

  it("recognizes its own entry by shape and reports it owned", async () => {
    await withFakeHome({}, async (home: FakeHome) => {
      await registerMcpServer({ hostId: JSON_HOST, home: home.home, serverPath: staged });
      expect(await inspectMcpServer({ hostId: JSON_HOST, home: home.home, serverPath: staged })).toBe(
        "owned",
      );
    });
  });

  it("reports absent for the command-line host, whose store it does not read", async () => {
    await withFakeHome({}, async (home: FakeHome) => {
      expect(await inspectMcpServer({ hostId: CLI_HOST, home: home.home, serverPath: staged })).toBe(
        "absent",
      );
    });
  });
});

describe("removal across mechanisms", () => {
  it("deletes a JSON file the removal emptied rather than leaving a husk", async () => {
    await withFakeHome({}, async (home: FakeHome) => {
      await registerMcpServer({ hostId: JSON_HOST, home: home.home, serverPath: staged });
      expect(await home.isFile(JSON_PATH)).toBe(true);
      await removeMcpServer({ hostId: JSON_HOST, home: home.home, serverPath: staged });
      expect(await home.exists(JSON_PATH)).toBe(false);
    });
  });

  it("leaves a foreign same-named JSON entry untouched", async () => {
    const before = `${JSON.stringify({ [MCP_SERVERS_KEY]: { [MCP_SERVER_NAME]: { command: "someone" } } }, null, 2)}\n`;
    await withFakeHome({ files: { [JSON_PATH]: before } }, async (home: FakeHome) => {
      const result = await removeMcpServer({ hostId: JSON_HOST, home: home.home, serverPath: staged });
      expect(result.ok).toBe(true);
      expect(await home.read(JSON_PATH)).toBe(before);
    });
  });

  it("removes its own JSON entry and keeps a foreign one beside it", async () => {
    await withFakeHome(
      { files: { [JSON_PATH]: `${JSON.stringify({ [MCP_SERVERS_KEY]: { other: { command: "x" } } }, null, 2)}\n` } },
      async (home: FakeHome) => {
        await registerMcpServer({ hostId: JSON_HOST, home: home.home, serverPath: staged });
        await removeMcpServer({ hostId: JSON_HOST, home: home.home, serverPath: staged });
        expect(Object.keys(serversOf(await home.readJson(JSON_PATH)))).toEqual(["other"]);
      },
    );
  });
});