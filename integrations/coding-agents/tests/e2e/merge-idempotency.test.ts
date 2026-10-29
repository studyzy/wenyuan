import { describe, expect, it } from "vitest";
import { HOST_LIFECYCLES, orderedRegistrations } from "../../src/hosts/lifecycle";
import { MCP_SERVERS_KEY } from "../../src/mcp/mechanisms";
import { BACKUP_SUFFIX } from "../../src/write/backup";
import { OWNERSHIP_MARKER } from "../../src/write/marker";
import { declaredEvents, eventValue, ownEntryCount } from "../helpers/hooks";
import { NO_HOST_CLI, runInstaller, type FakeHome, type JsonDocument, withFakeHome } from "../helpers/fake-home";



/**
 * Merging rather than replacing: foreign entries and unrelated keys survive,
 * a second run stacks no duplicates, and a pre-existing file is backed up once.
 *
 * The JSON host carries the server-map shape; the TOML host's table merge is
 * covered by the per-host suite.
 */

interface HostFixture {
  id: string;
  config: string;
  mcp: string;
}

const HOSTS: HostFixture[] = [
  { id: "codebuddy", config: ".codebuddy/settings.json", mcp: ".codebuddy/.mcp.json" },
];

/** The stage-one event spelling each host declares, holding the foreign entry. */
function sessionStartEvent(hostId: string): string {
  return orderedRegistrations(HOST_LIFECYCLES[hostId]!)[0]!.registration.event;
}

const SEEDED_CONFIG = `${JSON.stringify(
  {
    model: "opus",
    hooks: {
      "session-start": [{ command: "echo foreign" }],
    },
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

describe("installing into a home whose host config already holds other content", () => {
  it.each(HOSTS)("keeps everything foreign in $id", async (host: HostFixture) => {
    // The seed's event spelling must match what the host declares, or the
    // foreign entry lands on a key the installer never touches and the suite
    // would pass for the wrong reason.
    const seeded: JsonDocument = { model: "opus", hooks: { [sessionStartEvent(host.id)]: [{ command: "echo foreign" }] } };
    const seededConfig = `${JSON.stringify(seeded, null, 2)}\n`;

    await withFakeHome(
      {
        files: { [host.config]: seededConfig, [host.mcp]: SEEDED_MCP },
        environment: { PATH: NO_HOST_CLI },
      },
      async (home: FakeHome) => {
        const result = await runInstaller(["install", host.id]);
        expect(result.status).toBe(0);

        const document: JsonDocument = await home.readJson(host.config);
        expect(document["model"]).toBe("opus");
        // The foreign entry is kept exactly as the host wrote it, in the same
        // list our own entry was appended to, and neither its spelling nor its
        // shape is rewritten to match ours.
        const seeded: unknown = eventValue(document, sessionStartEvent(host.id));
        expect(Array.isArray(seeded)).toBe(true);
        const foreign = (seeded as unknown[]).filter(
          (entry) => JSON.stringify(entry).includes("echo foreign"),
        );
        expect(foreign).toEqual([{ command: "echo foreign" }]);
        for (const event of declaredEvents(host.id)) {
          expect(ownEntryCount(document, event), `${host.id} ${event}`).toBe(1);
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
        environment: { PATH: NO_HOST_CLI },
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
        for (const event of declaredEvents(host.id)) {
          expect(ownEntryCount(afterSecond, event), `${host.id} ${event}`).toBe(1);
        }
        expect(Object.keys(mcpAfterSecond[MCP_SERVERS_KEY] as JsonDocument)).toHaveLength(2);
      },
    );
  });

  it("writes the backup once and never overwrites it", async () => {
    await withFakeHome(
      {
        files: { ".claude/settings.json": SEEDED_CONFIG },
        environment: { PATH: NO_HOST_CLI },
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
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
      expect(await home.exists(`.claude/settings.json${BACKUP_SUFFIX}`)).toBe(false);
    });
  });
});
