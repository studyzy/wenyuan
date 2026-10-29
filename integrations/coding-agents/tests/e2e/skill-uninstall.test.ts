import { describe, expect, it } from "vitest";
import { removeInstalledSkill, SKILL_DIRECTORY_NAME } from "../../src/hosts/skills";
import { installedSkillDirectoryFor, skillsRootFor } from "../../src/hosts/skill-locations";
import { NO_HOST_CLI, runInstaller, type FakeHome, withFakeHome } from "../helpers/fake-home";



/**
 * Uninstall removes exactly the fixed child directory under a host's skills
 * root, recursively and tolerating its absence. Anything else under that root
 * survives: a foreign sibling directory and the skills root itself are left in
 * place, and uninstalling a host that never had a skill is a non-error no-op.
 */

function installedDir(hostId: string, home: FakeHome): string {
  const directory: string | null = installedSkillDirectoryFor(hostId, home.home);
  if (directory === null) {
    throw new Error(`no installed skill directory for ${hostId}`);
  }
  return directory;
}

function relative(home: FakeHome, absolute: string): string {
  return absolute.startsWith(home.home) ? absolute.slice(home.home.length + 1) : absolute;
}

describe("uninstalling a host's skill", () => {
  it("removes exactly the fixed child directory and keeps a foreign sibling", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "codebuddy"])).status).toBe(0);
      const target: string = installedDir("codebuddy", home);
      await home.write(".codebuddy/skills/foreign/SKILL.md", "# foreign\n");

      expect((await runInstaller(["uninstall", "codebuddy"])).status).toBe(0);
      expect(await home.exists(relative(home, target))).toBe(false);
      expect(await home.isFile(".codebuddy/skills/foreign/SKILL.md")).toBe(true);
      expect(await home.exists(".codebuddy/skills")).toBe(true);
    });
  });

  it("is a non-error no-op when the skill directory is already absent", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      const outcome = await removeInstalledSkill("claude-code", home.home);
      expect(outcome.ok).toBe(true);
      expect(outcome.changed).toBe(false);
    });
  });

  it("keeps a host's own copy under a shared root when a sibling is uninstalled", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      // Two hosts that read the same shared root each keep a directory of their
      // own inside it, so one host's removal cannot take the other's copy.
      expect((await runInstaller(["install", "codex", "codebuddy"])).status).toBe(0);
      const root: string | null = skillsRootFor("codex", home.home);
      expect(root).not.toBeNull();
      const shared: string = relative(home, root as string);
      expect(await home.exists(`${shared}/${SKILL_DIRECTORY_NAME}`)).toBe(true);
    });
  });

  it("removes nothing for a host outside the map", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      const outcome = await removeInstalledSkill("not-a-host", home.home);
      expect(outcome.ok).toBe(true);
      expect(outcome.changed).toBe(false);
    });
  });
});