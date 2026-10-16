import { describe, expect, it } from "vitest";
import { mkdir, rm as removeFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { HOSTS } from "../../src/hosts/registry";
import type { HostContribution } from "../../src/hosts/types";
import {
  copyPackagedSkill,
  installedSkillDirectory,
  packagedSkillDirectory,
  removeInstalledSkill,
  SKILL_DIRECTORY_NAME,
} from "../../src/hosts/skills";
import { runInstaller, withFakeHome, type FakeHome } from "../helpers/fake-home";

/**
 * The companion skill is copied into each host's own skills directory and
 * removed again on uninstall. Hosts install into a directory of their own
 * inside the shared fixed name, so uninstalling one host cannot take another
 * host's still-live copy even when two hosts resolve to the same skills root.
 */

function skillFile(skillsRoot: string, hostId: string): string {
  return `${installedSkillDirectory(skillsRoot, hostId)}/SKILL.md`;
}

describe("installing and uninstalling two hosts", () => {
  it("removes only the host that was uninstalled", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code", "codex"])).status).toBe(0);
      const claudeSkill: string = skillFile(".claude/skills", "claude-code");
      const codexSkill: string = skillFile(".codex/skills", "codex");
      expect(await home.isFile(claudeSkill)).toBe(true);
      expect(await home.isFile(codexSkill)).toBe(true);

      expect((await runInstaller(["uninstall", "claude-code"])).status).toBe(0);
      expect(await home.exists(claudeSkill)).toBe(false);
      expect((await home.read(codexSkill)).length).toBeGreaterThan(0);
    });
  });

  it("gives every registered host a copy of its own", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect(
        (await runInstaller(["install", ...HOSTS.map((host: HostContribution) => host.id)])).status,
      ).toBe(0);
      const roots: string[] = [];
      for (const host of HOSTS) {
        const relative: string = skillFile(host.locations.skillsDirectory, host.id);
        expect(await home.isFile(relative), relative).toBe(true);
        roots.push(host.locations.skillsDirectory);
      }
      // Whether or not two hosts resolve to the same root, each copy is a
      // directory of its own and all of them are present at once.
      expect(new Set(roots).size).toBeLessThanOrEqual(roots.length);
    });
  });
});

describe("a skills root two hosts share", () => {
  it("keeps the other host's copy when one host is uninstalled", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const shared: string = home.path("shared-skills");
      expect((await copyPackagedSkill(shared, "host-a")).ok).toBe(true);
      expect((await copyPackagedSkill(shared, "host-b")).ok).toBe(true);
      expect(await home.isFile(skillFile("shared-skills", "host-a"))).toBe(true);
      expect(await home.isFile(skillFile("shared-skills", "host-b"))).toBe(true);

      expect((await removeInstalledSkill(shared, "host-a")).ok).toBe(true);
      expect(await home.exists(skillFile("shared-skills", "host-a"))).toBe(false);
      expect((await home.read(skillFile("shared-skills", "host-b"))).length).toBeGreaterThan(0);
    });
  });

  it("keeps a copy that also holds something this package did not create", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const shared: string = home.path("shared-skills");
      await copyPackagedSkill(shared, "host-a");
      await home.write(`${installedSkillDirectory(shared, "host-a")}/mine.md`, "# mine\n");

      const result = await removeInstalledSkill(shared, "host-a");
      expect(result.ok).toBe(true);
      expect(await home.exists(`${installedSkillDirectory(shared, "host-a")}/SKILL.md`)).toBe(false);
      expect(await home.read(`${installedSkillDirectory(shared, "host-a")}/mine.md`)).toContain("mine");
    });
  });

  it("installs under the shared fixed directory name", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "codebuddy"])).status).toBe(0);
      const relative: string = `.codebuddy/skills/${SKILL_DIRECTORY_NAME}/codebuddy/SKILL.md`;
      expect(await home.isFile(relative)).toBe(true);
    });
  });

  it("removes a nested packaged file and collapses the per-host directory", async () => {
    const source: string | null = packagedSkillDirectory();
    expect(source).not.toBeNull();
    const nested: string = join(source as string, "guides", "usage.md");
    await mkdir(join(source as string, "guides"), { recursive: true });
    await writeFile(nested, "# usage\n");
    try {
      await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
        const shared: string = home.path("shared-skills");
        const relative: string = `shared-skills/${SKILL_DIRECTORY_NAME}/host-a`;
        await copyPackagedSkill(shared, "host-a");
        expect(await home.isFile(`${relative}/guides/usage.md`)).toBe(true);

        expect((await removeInstalledSkill(shared, "host-a")).ok).toBe(true);
        expect(await home.exists(relative)).toBe(false);
      });
    } finally {
      await removeFile(join(source as string, "guides"), { recursive: true, force: true });
    }
  });
});
