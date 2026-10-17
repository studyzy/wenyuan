import { describe, expect, it } from "vitest";
import { HOST_LIFECYCLES, orderedRegistrations } from "../../src/hosts/lifecycle";
import { MCP_SERVERS_KEY, MCP_SERVER_NAME } from "../../src/mcp/register";
import { PER_USER_DIRECTORY_NAME } from "../../src/runtime/layout";
import { OWNERSHIP_MARKER } from "../../src/write/marker";
import { declaredEvents } from "../helpers/hooks";
import { runInstaller, withFakeHome, type FakeHome, type JsonDocument } from "../helpers/fake-home";

/**
 * Uninstall removes exactly what install wrote: its own hook entries, its own
 * MCP entry and its own skill copy, leaving foreign entries and unrelated
 * top-level keys in place. A container the removal emptied is deleted, and a
 * file left holding nothing meaningful goes away rather than staying a husk.
 */

const CONFIG = ".claude/settings.json";
const MCP = ".claude.json";
const SKILL = `.claude/skills/${OWNERSHIP_MARKER}/claude-code/SKILL.md`;

/**
 * The event spellings this host declares, and the first stage's spelling — the
 * one whose event also carries a foreign entry seeded below.
 */
const DECLARED_EVENTS = declaredEvents("claude-code");
const SESSION_START_EVENT = orderedRegistrations(HOST_LIFECYCLES["claude-code"]!)[0]!.registration.event;
const SURVIVING_EVENTS = DECLARED_EVENTS.filter((event) => event !== SESSION_START_EVENT);

const SEEDED_CONFIG = `${JSON.stringify(
  { model: "opus", hooks: { [SESSION_START_EVENT]: [{ command: "echo foreign" }] } },
  null,
  2,
)}\n`;

const SEEDED_MCP = `${JSON.stringify(
  { theme: "dark", mcpServers: { "other-server": { command: "other" } } },
  null,
  2,
)}\n`;

function hooksOf(document: JsonDocument): JsonDocument {
  const hooks: unknown = document["hooks"];
  return hooks !== null && typeof hooks === "object" ? (hooks as JsonDocument) : {};
}

function serversOf(document: JsonDocument): JsonDocument {
  const servers: unknown = document[MCP_SERVERS_KEY];
  return servers !== null && typeof servers === "object" ? (servers as JsonDocument) : {};
}

describe("uninstalling a home seeded with foreign content", () => {
  it("removes its own entries and nothing else", async () => {
    await withFakeHome(
      { files: { [CONFIG]: SEEDED_CONFIG, [MCP]: SEEDED_MCP }, environment: { PATH: "" } },
      async (home: FakeHome) => {
        expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
        expect(await home.isFile(SKILL)).toBe(true);

        const result = await runInstaller(["uninstall", "claude-code"]);
        expect(result.status).toBe(0);

        const document: JsonDocument = await home.readJson(CONFIG);
        expect(document["model"]).toBe("opus");
        const hooks: JsonDocument = hooksOf(document);
        // The event that also carried a foreign entry stays, with that entry.
        expect(hooks[SESSION_START_EVENT]).toEqual([{ command: "echo foreign" }]);
        // The events that held nothing but its own entries were emptied and
        // deleted rather than left as empty arrays.
        for (const event of SURVIVING_EVENTS) {
          expect(hooks[event]).toBeUndefined();
        }

        const mcp: JsonDocument = await home.readJson(MCP);
        expect(mcp["theme"]).toBe("dark");
        expect(Object.keys(serversOf(mcp))).toEqual(["other-server"]);
        expect(serversOf(mcp)["other-server"]).toEqual({ command: "other" });

        expect(await home.exists(SKILL)).toBe(false);
      },
    );
  });

  it("leaves no marker in anything it wrote", async () => {
    await withFakeHome(
      { files: { [CONFIG]: SEEDED_CONFIG, [MCP]: SEEDED_MCP }, environment: { PATH: "" } },
      async (home: FakeHome) => {
        expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
        expect((await runInstaller(["uninstall", "claude-code"])).status).toBe(0);

        for (const relative of await home.listFiles()) {
          // The staged runtime directory carries the marker in its own name by
          // design, so only what the installer wrote into the host is checked.
          if (relative.startsWith(PER_USER_DIRECTORY_NAME)) {
            continue;
          }
          expect(await home.read(relative), relative).not.toContain(OWNERSHIP_MARKER);
        }
      },
    );
  });
});

describe("uninstalling a home that held nothing else", () => {
  it("deletes the files its removal emptied", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
      expect(await home.isFile(CONFIG)).toBe(true);
      expect(await home.isFile(MCP)).toBe(true);

      expect((await runInstaller(["uninstall", "claude-code"])).status).toBe(0);
      expect(await home.exists(CONFIG)).toBe(false);
      expect(await home.exists(MCP)).toBe(false);
      expect(await home.exists(`.claude/skills/${OWNERSHIP_MARKER}`)).toBe(false);
    });
  });

  it("leaves the staged runtime alone", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
      const staged: string[] = (await home.listFiles(PER_USER_DIRECTORY_NAME)).sort();
      expect(staged.length).toBeGreaterThan(0);

      expect((await runInstaller(["uninstall", "claude-code"])).status).toBe(0);
      expect((await home.listFiles(PER_USER_DIRECTORY_NAME)).sort()).toEqual(staged);
    });
  });

  it("sweeps every candidate location a host accepts", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      // Codex accepts two MCP locations; a dormant duplicate in the second one
      // is removed all the same, while what else that file holds survives.
      await home.write(
        ".codex/settings.json",
        `${JSON.stringify(
          {
            model: "opus",
            mcpServers: {
              [MCP_SERVER_NAME]: {
                command: "node",
                args: [`${home.home}/.wenyuan/${OWNERSHIP_MARKER}/stale.mjs`],
              },
            },
          },
          null,
          2,
        )}\n`,
      );
      expect((await runInstaller(["uninstall", "codex"])).status).toBe(0);
      const swept: JsonDocument = await home.readJson(".codex/settings.json");
      expect(swept["model"]).toBe("opus");
      expect(Object.keys(serversOf(swept))).toEqual([]);
    });
  });
});
