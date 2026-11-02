import { describe, expect, it } from "vitest";
import {
  MCP_HARNESS_ENV,
  MCP_SERVER_NAME,
  MCP_SERVERS_KEY,
  WORKBUDDY_MCP_PATH,
} from "../../src/mcp/mechanisms";
import { runtimeDirectory } from "../../src/runtime/layout";
import { OWNERSHIP_MARKER } from "../../src/write/marker";
import { declaredEvents, eventCommandEntries, eventValue } from "../helpers/hooks";
import { NO_HOST_CLI, runInstaller, type FakeHome, type JsonDocument, withFakeHome } from "../helpers/fake-home";

/**
 * WorkBuddy: the one host whose MCP registration is a single fixed file.
 *
 * These cases pin what is specific to WorkBuddy rather than re-testing the
 * shared machinery: the settings file is edited as text so a hand-maintained
 * comment survives, the MCP entry lands at `~/.workbuddy/mcp.json` and nowhere
 * else, and a foreign shape on an event is reported instead of claimed.
 */

const SETTINGS = ".workbuddy/settings.json";
const SKILL = `.workbuddy/skills/${OWNERSHIP_MARKER}/SKILL.md`;

/** A settings file as a person would leave it: a comment, a member, a foreign hook. */
const HAND_MAINTAINED = `{
  // kept by hand: this must survive an install
  "theme": "light",
  "hooks": {
    "PreToolUse": [
      { "matcher": "Bash", "hooks": [{ "type": "command", "command": "echo audit" }] }
    ]
  }
}
`;

describe("installing into a bare home", () => {
  it("writes the three events, the server entry and the skill at WorkBuddy's own paths", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "workbuddy"])).status).toBe(0);

      const settings: JsonDocument = await home.readJson(SETTINGS);
      const staged: string = runtimeDirectory();
      for (const event of declaredEvents("workbuddy")) {
        const entries = eventCommandEntries(settings, event);
        expect(entries, event).toHaveLength(1);
        expect(entries[0]!.command).toContain(`${staged}/hooks/`);
      }

      const mcp: JsonDocument = await home.readJson(WORKBUDDY_MCP_PATH);
      const entry = (mcp[MCP_SERVERS_KEY] as JsonDocument)[MCP_SERVER_NAME] as JsonDocument;
      expect(entry["command"]).toBe("node");
      expect((entry["env"] as JsonDocument)[MCP_HARNESS_ENV]).toBe("workbuddy");

      expect(await home.isFile(SKILL)).toBe(true);
    });
  });

  it("leaves the CodeBuddy-style alternates unwritten", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "workbuddy"])).status).toBe(0);
      // WorkBuddy reads one path and no fallback, so a dotted alternate or a
      // home-level file would be a registration it never opens.
      expect(await home.exists(".workbuddy/.mcp.json")).toBe(false);
      expect(await home.exists(".workbuddy.json")).toBe(false);
      expect(await home.isFile(WORKBUDDY_MCP_PATH)).toBe(true);
    });
  });
});

describe("installing over a hand-maintained settings file", () => {
  it("keeps the comment and the members it does not own", async () => {
    await withFakeHome(
      { files: { [SETTINGS]: HAND_MAINTAINED }, environment: { PATH: NO_HOST_CLI } },
      async (home: FakeHome) => {
        expect((await runInstaller(["install", "workbuddy"])).status).toBe(0);
        const written: string = await home.read(SETTINGS);
        expect(written).toContain("// kept by hand: this must survive an install");
        expect(written).toContain('"theme": "light"');
        expect(written).toContain("echo audit");
        expect(written).toContain("session-start.mjs");
      },
    );
  });

  it("removes only what it wrote on uninstall, comment included", async () => {
    await withFakeHome(
      { files: { [SETTINGS]: HAND_MAINTAINED }, environment: { PATH: NO_HOST_CLI } },
      async (home: FakeHome) => {
        expect((await runInstaller(["install", "workbuddy"])).status).toBe(0);
        expect((await runInstaller(["uninstall", "workbuddy"])).status).toBe(0);

        const written: string = await home.read(SETTINGS);
        expect(written).toContain("// kept by hand: this must survive an install");
        expect(written).toContain("echo audit");
        expect(written).not.toContain(OWNERSHIP_MARKER);

        expect(await home.exists(WORKBUDDY_MCP_PATH)).toBe(false);
        expect(await home.exists(`.workbuddy/skills/${OWNERSHIP_MARKER}`)).toBe(false);
      },
    );
  });

  it("deletes the settings file it emptied and nothing else", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "workbuddy"])).status).toBe(0);
      expect((await runInstaller(["uninstall", "workbuddy"])).status).toBe(0);
      expect(await home.exists(SETTINGS)).toBe(false);
      expect(await home.exists(WORKBUDDY_MCP_PATH)).toBe(false);
    });
  });
});

describe("a foreign shape on an event WorkBuddy must run", () => {
  it("reports the host as not wired instead of claiming the registration", async () => {
    await withFakeHome(
      {
        files: {
          [SETTINGS]: `${JSON.stringify(
            { hooks: { SessionStart: { command: "echo foreign" } } },
            null,
            2,
          )}\n`,
        },
        environment: { PATH: NO_HOST_CLI },
      },
      async (home: FakeHome) => {
        const result = await runInstaller(["install", "workbuddy"]);
        expect(result.status).not.toBe(0);
        expect(result.combined()).toContain("WorkBuddy was not wired");

        // The foreign value is exactly as the host wrote it: the other two
        // events were still added, so the file is not left untouched either.
        const settings: JsonDocument = await home.readJson(SETTINGS);
        expect(eventValue(settings, "SessionStart")).toEqual({ command: "echo foreign" });
        expect(eventCommandEntries(settings, "Stop")).toHaveLength(1);
      },
    );
  });
});
