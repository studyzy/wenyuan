import { describe, expect, it } from "vitest";
import { LIFECYCLE_EVENTS } from "../../src/hosts/hooks";
import { MCP_SERVERS_KEY } from "../../src/mcp/register";
import { BACKUP_SUFFIX } from "../../src/write/backup";
import { OWNERSHIP_MARKER } from "../../src/write/marker";
import { runInstaller, withFakeHome, type FakeHome, type JsonDocument } from "../helpers/fake-home";

/**
 * Merging rather than replacing: foreign entries and unrelated keys survive,
 * a second run stacks no duplicates, and a pre-existing file is backed up once.
 */

interface HostFixture {
  id: string;
  config: string;
  mcp: string;
}

const HOSTS: HostFixture[] = [
  { id: "claude-code", config: ".claude/settings.json", mcp: ".claude.json" },
  { id: "codex", config: ".codex/settings.json", mcp: ".codex/mcp.json" },
];

const SEEDED_CONFIG = `${JSON.stringify(
  {
    model: "opus",
    hooks: { "session-start": [{ command: "echo foreign" }] },
  },
  null,
  2,
)}\n`;

const SEEDED_MCP = `${JSON.stringify(
  {
    theme: "dark",
    mcpServers: { "other-server": { command: "other", args: ["/opt/other"] } },
  },
  null,
  2,
)}\n`;

function eventEntries(document: JsonDocument, eventId: string): unknown[] {
  const hooks: unknown = document["hooks"];
  if (hooks === null || typeof hooks !== "object") {
    return [];
  }
  const entries: unknown = (hooks as JsonDocument)[eventId];
  return Array.isArray(entries) ? entries : [];
}

function ownEntryCount(document: JsonDocument, eventId: string): number {
  return eventEntries(document, eventId).filter(
    (entry) => entry !== null && typeof entry === "object" && JSON.stringify(entry).includes(OWNERSHIP_MARKER),
  ).length;
}

describe("installing into a home whose host config already holds other content", () => {
  it.each(HOSTS)("keeps everything foreign in $id", async (host: HostFixture) => {
    await withFakeHome(
      {
        files: { [host.config]: SEEDED_CONFIG, [host.mcp]: SEEDED_MCP },
        environment: { PATH: "" },
      },
      async (home: FakeHome) => {
        const result = await runInstaller(["install", host.id]);
        expect(result.status).toBe(0);

        const document: JsonDocument = await home.readJson(host.config);
        expect(document["model"]).toBe("opus");
        const sessionStart: unknown[] = eventEntries(document, "session-start");
        expect(sessionStart).toHaveLength(2);
        expect(JSON.stringify(sessionStart)).toContain("echo foreign");
        expect(ownEntryCount(document, "session-start")).toBe(1);
        for (const event of LIFECYCLE_EVENTS) {
          expect(ownEntryCount(document, event.id), `${host.id} ${event.id}`).toBe(1);
        }

        const mcp: JsonDocument = await home.readJson(host.mcp);
        expect(mcp["theme"]).toBe("dark");
        const servers: JsonDocument = mcp[MCP_SERVERS_KEY] as JsonDocument;
        expect(Object.keys(servers).sort()).toEqual([OWNERSHIP_MARKER, "other-server"].sort());
        expect(servers["other-server"]).toEqual({ command: "other", args: ["/opt/other"] });
      },
    );
  });
});

describe("installing the same host twice", () => {
  it.each(HOSTS)("stacks nothing in $id", async (host: HostFixture) => {
    await withFakeHome(
      {
        files: { [host.config]: SEEDED_CONFIG, [host.mcp]: SEEDED_MCP },
        environment: { PATH: "" },
      },
      async (home: FakeHome) => {
        expect((await runInstaller(["install", host.id])).status).toBe(0);
        const afterFirst: JsonDocument = await home.readJson(host.config);
        const mcpAfterFirst: JsonDocument = await home.readJson(host.mcp);

        expect((await runInstaller(["install", host.id])).status).toBe(0);
        const afterSecond: JsonDocument = await home.readJson(host.config);
        const mcpAfterSecond: JsonDocument = await home.readJson(host.mcp);

        expect(afterSecond).toEqual(afterFirst);
        expect(mcpAfterSecond).toEqual(mcpAfterFirst);
        for (const event of LIFECYCLE_EVENTS) {
          expect(ownEntryCount(afterSecond, event.id), `${host.id} ${event.id}`).toBe(1);
        }
        expect(Object.keys(mcpAfterSecond[MCP_SERVERS_KEY] as JsonDocument)).toHaveLength(2);
      },
    );
  });

  it("writes the backup once and never overwrites it", async () => {
    await withFakeHome(
      {
        files: { ".claude/settings.json": SEEDED_CONFIG },
        environment: { PATH: "" },
      },
      async (home: FakeHome) => {
        const backup: string = `.claude/settings.json${BACKUP_SUFFIX}`;
        expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
        expect(await home.read(backup)).toBe(SEEDED_CONFIG);

        expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
        expect(await home.read(backup)).toBe(SEEDED_CONFIG);
      },
    );
  });

  it("writes no backup for a file that did not exist before", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
      expect(await home.exists(`.claude/settings.json${BACKUP_SUFFIX}`)).toBe(false);
    });
  });
});
