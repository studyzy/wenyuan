import { describe, expect, it } from "vitest";
import {
  candidatePaths,
  registerJsonServerMap,
  removeJsonServerMap,
  resolveCandidate,
} from "../../src/mcp/json-server-map";
import {
  JSON_HOST_CANDIDATES,
  MCP_HARNESS_ENV,
  MCP_SERVER_NAME,
  MCP_SERVERS_KEY,
} from "../../src/mcp/mechanisms";
import { BACKUP_SUFFIX } from "../../src/write/backup";
import { withFakeHome, type FakeHome, type JsonDocument } from "../helpers/fake-home";

/**
 * The JSON host's mechanism: the priority-ordered candidate lookup, the
 * one-member merge that survives a foreign server beside it, the defensive
 * read, the sweep of every candidate, and the emptied-container deletion.
 */

const hostId = "codebuddy";
const staged = "/home/tester/.wenyuan/wenyuan-memory/mcp-server.mjs";
const [PRIMARY, SECONDARY] = JSON_HOST_CANDIDATES as [string, string, ...string[]];

function serversOf(document: JsonDocument): JsonDocument {
  const servers: unknown = document[MCP_SERVERS_KEY];
  return servers !== null && typeof servers === "object" && !Array.isArray(servers)
    ? (servers as JsonDocument)
    : {};
}

describe("resolving the candidate location", () => {
  it("lists the declared candidates in priority order", () => {
    expect(candidatePaths(hostId, "/home/tester")).toEqual(
      JSON_HOST_CANDIDATES.map((candidate) => `/home/tester/${candidate}`),
    );
  });

  it("picks the first candidate that exists, else the highest-priority one", async () => {
    await withFakeHome({ files: { [SECONDARY]: "{}\n" } }, async (home: FakeHome) => {
      const paths = candidatePaths(hostId, home.home);
      expect(resolveCandidate(paths)).toBe(home.path(SECONDARY));
    });
    await withFakeHome({}, async (home: FakeHome) => {
      const paths = candidatePaths(hostId, home.home);
      expect(resolveCandidate(paths)).toBe(home.path(PRIMARY));
    });
  });
});

describe("installing into the JSON config", () => {
  it("writes the first existing candidate and not the top one", async () => {
    await withFakeHome({ files: { [SECONDARY]: '{\n  "theme": "dark"\n}\n' } }, async (home) => {
      const result = await registerJsonServerMap({ home: home.home, serverPath: staged, hostId });
      expect(result.ok).toBe(true);
      expect(await home.exists(PRIMARY)).toBe(false);
      const document = await home.readJson(SECONDARY);
      expect(document["theme"]).toBe("dark");
      expect(Object.keys(serversOf(document))).toEqual([MCP_SERVER_NAME]);
    });
  });

  it("falls back to the highest-priority candidate when none exists", async () => {
    await withFakeHome({}, async (home: FakeHome) => {
      await registerJsonServerMap({ home: home.home, serverPath: staged, hostId });
      expect(await home.isFile(PRIMARY)).toBe(true);
      const document = await home.readJson(PRIMARY);
      expect(serversOf(document)[MCP_SERVER_NAME]).toMatchObject({ command: "node", args: [staged] });
    });
  });

  it("names the calling host in the entry's environment", async () => {
    await withFakeHome({}, async (home: FakeHome) => {
      await registerJsonServerMap({ home: home.home, serverPath: staged, hostId });
      const entry = serversOf(await home.readJson(PRIMARY))[MCP_SERVER_NAME] as JsonDocument;
      const env = entry["env"] as JsonDocument;
      expect(env[MCP_HARNESS_ENV]).toBe(hostId);
    });
  });

  it("keeps a foreign server beside its own", async () => {
    await withFakeHome(
      { files: { [PRIMARY]: `${JSON.stringify({ mcpServers: { "other": { command: "x" } } }, null, 2)}\n` } },
      async (home: FakeHome) => {
        await registerJsonServerMap({ home: home.home, serverPath: staged, hostId });
        const servers = serversOf(await home.readJson(PRIMARY));
        expect(Object.keys(servers).sort()).toEqual([MCP_SERVER_NAME, "other"].sort());
      },
    );
  });

  it("refuses a foreign same-named entry and writes nothing", async () => {
    const before = `${JSON.stringify({ mcpServers: { [MCP_SERVER_NAME]: { command: "python" } } }, null, 2)}\n`;
    await withFakeHome({ files: { [PRIMARY]: before } }, async (home: FakeHome) => {
      const result = await registerJsonServerMap({ home: home.home, serverPath: staged, hostId });
      expect(result.ok).toBe(false);
      expect(result.written).toBe(false);
      expect(await home.read(PRIMARY)).toBe(before);
    });
  });

  it.each([
    ["a missing file", undefined],
    ["an unparseable file", "not json at all"],
    ["a non-object top level", "[1, 2, 3]\n"],
  ])("treats %s as empty and writes a fresh document", async (_name: string, seed?: string) => {
    const files = seed === undefined ? {} : { [PRIMARY]: seed };
    await withFakeHome({ files }, async (home: FakeHome) => {
      const result = await registerJsonServerMap({ home: home.home, serverPath: staged, hostId });
      expect(result.ok).toBe(true);
      const document = await home.readJson(PRIMARY);
      expect(Object.keys(serversOf(document))).toEqual([MCP_SERVER_NAME]);
    });
  });

  it("is idempotent on a second run", async () => {
    await withFakeHome({}, async (home: FakeHome) => {
      await registerJsonServerMap({ home: home.home, serverPath: staged, hostId });
      const after = await home.read(PRIMARY);
      await registerJsonServerMap({ home: home.home, serverPath: staged, hostId });
      expect(await home.read(PRIMARY)).toBe(after);
    });
  });

  it("writes the sibling backup once", async () => {
    const before = `${JSON.stringify({ theme: "dark" }, null, 2)}\n`;
    await withFakeHome({ files: { [PRIMARY]: before } }, async (home: FakeHome) => {
      await registerJsonServerMap({ home: home.home, serverPath: staged, hostId });
      expect(await home.read(`${PRIMARY}${BACKUP_SUFFIX}`)).toBe(before);
    });
  });
});

describe("sweeping the candidate list on removal", () => {
  it("removes our entry from every candidate and deletes the ones it emptied", async () => {
    await withFakeHome({}, async (home: FakeHome) => {
      await registerJsonServerMap({ home: home.home, serverPath: staged, hostId });
      // A dormant duplicate a previous version left in the second candidate.
      await home.write(
        SECONDARY,
        `${JSON.stringify({ mcpServers: { [MCP_SERVER_NAME]: { command: "node", args: [staged] } } }, null, 2)}\n`,
      );
      const result = await removeJsonServerMap({ home: home.home, serverPath: staged, hostId });
      expect(result.ok).toBe(true);
      expect(await home.exists(PRIMARY)).toBe(false);
      expect(await home.exists(SECONDARY)).toBe(false);
    });
  });

  it("keeps a container that still holds a foreign entry", async () => {
    await withFakeHome(
      { files: { [PRIMARY]: `${JSON.stringify({ mcpServers: { other: { command: "x" } } }, null, 2)}\n` } },
      async (home: FakeHome) => {
        await registerJsonServerMap({ home: home.home, serverPath: staged, hostId });
        await removeJsonServerMap({ home: home.home, serverPath: staged, hostId });
        expect(Object.keys(serversOf(await home.readJson(PRIMARY)))).toEqual(["other"]);
      },
    );
  });

  it("leaves a foreign same-named entry alone", async () => {
    const before = `${JSON.stringify({ mcpServers: { [MCP_SERVER_NAME]: { command: "python" } } }, null, 2)}\n`;
    await withFakeHome({ files: { [PRIMARY]: before } }, async (home: FakeHome) => {
      const result = await removeJsonServerMap({ home: home.home, serverPath: staged, hostId });
      expect(result.ok).toBe(true);
      expect(await home.read(PRIMARY)).toBe(before);
    });
  });
});