import { describe, expect, it } from "vitest";
import { join } from "node:path";
import { SKILL_DIRECTORY_NAME } from "../../src/hosts/skills";
import { skillsRootFor } from "../../src/hosts/skill-locations";
import { refreshInstalledSkill } from "../../src/runtime/skill-refresh";
import { runInstaller, withFakeHome, type FakeHome } from "../helpers/fake-home";

/**
 * The per-session companion-skill refresh. A host the skill is installed for is
 * re-copied in place once its installed copy drifts from the packaged one, so an
 * upgraded package reaches the installed skill without a re-install. A host the
 * skill was never installed for is left alone: the refresh never introduces a
 * skill where none was placed.
 */

/** The installed skill file as a home-relative path, the form FakeHome reads. */
function skillFile(hostId: string, home: FakeHome): string {
  const root: string | null = skillsRootFor(hostId, home.home);
  if (root === null) {
    throw new Error(`no skills root declared for ${hostId}`);
  }
  return join(root, SKILL_DIRECTORY_NAME, "SKILL.md").slice(home.home.length + 1);
}

describe("refreshing an installed skill on drift", () => {
  it("restores the packaged content over a drifted installed copy", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
      const installed: string = skillFile("claude-code", home);
      const packaged: string = await home.read(installed);

      await home.write(installed, "# drifted\n");
      const outcome = await refreshInstalledSkill("claude-code", home.home);
      expect(outcome.refreshed).toBe(true);
      expect(await home.read(installed)).toBe(packaged);
    });
  });

  it("is a no-op when the installed copy already matches the packaged one", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "codebuddy"])).status).toBe(0);
      const outcome = await refreshInstalledSkill("codebuddy", home.home);
      expect(outcome.refreshed).toBe(false);
    });
  });

  it("never introduces a skill for a host that has none installed", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      // Nothing was ever installed, so the skills root does not exist either.
      const outcome = await refreshInstalledSkill("codex", home.home);
      expect(outcome.refreshed).toBe(false);
      expect(await home.exists(".agents/skills")).toBe(false);
    });
  });

  it("never reintroduces a skill that was uninstalled", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "codebuddy"])).status).toBe(0);
      expect((await runInstaller(["uninstall", "codebuddy"])).status).toBe(0);

      const outcome = await refreshInstalledSkill("codebuddy", home.home);
      expect(outcome.refreshed).toBe(false);
      expect(await home.exists(`.codebuddy/skills/${SKILL_DIRECTORY_NAME}`)).toBe(false);
    });
  });

  it("does nothing for a host outside the declared map", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const outcome = await refreshInstalledSkill("not-a-host", home.home);
      expect(outcome.refreshed).toBe(false);
    });
  });

  it("leaves a host's own copy untouched when a sibling drifts", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code", "codebuddy"])).status).toBe(0);
      const claude: string = skillFile("claude-code", home);
      const buddy: string = skillFile("codebuddy", home);
      const buddyPackaged: string = await home.read(buddy);

      await home.write(claude, "# drifted\n");
      const outcome = await refreshInstalledSkill("claude-code", home.home);
      expect(outcome.refreshed).toBe(true);
      expect(await home.read(buddy)).toBe(buddyPackaged);
    });
  });
});