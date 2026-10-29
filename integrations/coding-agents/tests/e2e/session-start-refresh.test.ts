import { describe, expect, it } from "vitest";
import { join } from "node:path";
import { SKILL_DIRECTORY_NAME } from "../../src/hosts/skills";
import { skillsRootFor } from "../../src/hosts/skill-locations";
import { NO_HOST_CLI, runInstaller, type FakeHome, withFakeHome } from "../helpers/fake-home";
import { runHook, seedStoredConnection } from "../helpers/hook-run";



/**
 * The packaged session-start hook refreshes the host's installed companion skill
 * as a side effect of a firing. Launching it the way a host does — by absolute
 * path, with the lifecycle payload on standard input — re-copies the installed
 * skill when it has drifted, and the refresh never reaches the session envelope.
 */

/** The installed skill file as a home-relative path, the form FakeHome reads. */
function skillFile(hostId: string, home: FakeHome): string {
  const root: string | null = skillsRootFor(hostId, home.home);
  if (root === null) {
    throw new Error(`no skills root declared for ${hostId}`);
  }
  return join(root, SKILL_DIRECTORY_NAME, "SKILL.md").slice(home.home.length + 1);
}

describe("the session-start hook refreshes the installed skill", () => {
  it("restores a drifted skill when the session starts with the service down", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
      const installed: string = skillFile("claude-code", home);
      const packaged: string = await home.read(installed);
      await home.write(installed, "# drifted\n");

      // The service is unreachable, so the session emits nothing; the refresh
      // still runs and must not be disturbed by the failed recall.
      await seedStoredConnection(home, "http://127.0.0.1:1");
      const run = await runHook({
        home,
        scriptFile: "session-start.mjs",
        payload: { session_id: "s-1", host: "claude-code" },
      });
      expect(run.status).toBe(0);
      expect(run.envelope()).toBeNull();
      expect(await home.read(installed)).toBe(packaged);
    });
  });

  it("does not place a skill for a host that has none installed", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      await seedStoredConnection(home, "http://127.0.0.1:1");
      const run = await runHook({
        home,
        scriptFile: "session-start.mjs",
        payload: { session_id: "s-1", host: "codebuddy" },
      });
      expect(run.status).toBe(0);
      expect(await home.exists(`.codebuddy/skills/${SKILL_DIRECTORY_NAME}`)).toBe(false);
    });
  });

  it("emits the same envelope whether or not a refresh was needed", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
      await seedStoredConnection(home, "http://127.0.0.1:1");

      const first = await runHook({
        home,
        scriptFile: "session-start.mjs",
        payload: { session_id: "s-1", host: "claude-code" },
      });
      const second = await runHook({
        home,
        scriptFile: "session-start.mjs",
        payload: { session_id: "s-2", host: "claude-code" },
      });
      expect(first.status).toBe(0);
      expect(second.status).toBe(0);
      expect(first.envelope()).toEqual(second.envelope());
    });
  });
});