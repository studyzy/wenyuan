import { describe, expect, it } from "vitest";
import { HOOK_SCRIPT_FILES } from "../../src/hosts/lifecycle";
import { MCP_SERVERS_KEY, MCP_SERVER_NAME } from "../../src/mcp/register";
import { HOOKS_SUBDIRECTORY, PER_USER_DIRECTORY_NAME, RUNTIME_DIRECTORY_NAME, runtimeDirectory } from "../../src/runtime/layout";
import { OWNERSHIP_MARKER } from "../../src/write/marker";
import { declaredEvents, eventCommands } from "../helpers/hooks";
import { runInstaller, withFakeHome, type FakeHome, type JsonDocument } from "../helpers/fake-home";

/**
 * Staging: the runtime is copied into the stable per-user directory before any
 * host is wired, and every command path a host config carries points at that
 * copy rather than at the evictable package location the command ran from.
 *
 * The directory's own name keeps the marker, which is what lets a later
 * uninstall still recognize the entries.
 */

const CONFIG = ".claude/settings.json";
const MCP = ".claude.json";

function commandsIn(config: JsonDocument, hostId: string): string[] {
  return declaredEvents(hostId).flatMap((event) => eventCommands(config, event));
}

describe("installing from a transient package location", () => {
  it("points every command path at the stable runtime directory", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code"])).status).toBe(0);

      const staged: string = runtimeDirectory();
      expect(staged.startsWith(home.home)).toBe(true);
      expect(RUNTIME_DIRECTORY_NAME).toContain(OWNERSHIP_MARKER);

      const commands: string[] = commandsIn(await home.readJson(CONFIG), "claude-code");
      expect(commands).toHaveLength(declaredEvents("claude-code").length);
      for (const command of commands) {
        expect(command).toContain(staged);
        expect(command).not.toContain(home.packageRoot);
      }

      const servers: JsonDocument = (await home.readJson(MCP))[MCP_SERVERS_KEY] as JsonDocument;
      const args: unknown = (servers[MCP_SERVER_NAME] as JsonDocument)["args"];
      expect(Array.isArray(args)).toBe(true);
      for (const argument of args as string[]) {
        expect(argument).toContain(staged);
        expect(argument).not.toContain(home.packageRoot);
      }
    });
  });

  it("stages the packaged entry points under a marker-bearing directory", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
      const staged: string[] = await home.listFiles(PER_USER_DIRECTORY_NAME);
      for (const scriptFile of Object.values(HOOK_SCRIPT_FILES)) {
        expect(staged).toContain(
          `${PER_USER_DIRECTORY_NAME}/${RUNTIME_DIRECTORY_NAME}/${HOOKS_SUBDIRECTORY}/${scriptFile}`,
        );
      }
    });
  });

  it("leaves entries a later uninstall still recognizes", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
      expect((await runInstaller(["uninstall", "claude-code"])).status).toBe(0);
      expect(await home.exists(CONFIG)).toBe(false);
      expect(await home.exists(MCP)).toBe(false);
    });
  });
});
