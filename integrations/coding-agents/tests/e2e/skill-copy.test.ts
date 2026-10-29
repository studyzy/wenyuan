import { describe, expect, it } from "vitest";
import { mkdir, rm as removeFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { HOSTS } from "../../src/hosts/registry";
import type { HostContribution } from "../../src/hosts/types";
import {
  copyPackagedSkill,
  packagedSkillDirectory,
  removeInstalledSkill,
  SKILL_DIRECTORY_NAME,
} from "../../src/hosts/skills";
import { skillsRootFor } from "../../src/hosts/skill-locations";
import { runInstaller, withFakeHome, withRestrictedPath, writeStandInDsh, type FakeHome } from "../helpers/fake-home";

/**
 * The companion skill is copied into each host's own skills directory under one
 * fixed child-directory name and removed again on uninstall. The name is the
 * same for every host, and the copy overwrites in place, so a repeated install
 * never nests or duplicates it.
 */

/** The installed skill file as a home-relative path, the form FakeHome reads. */
function skillFile(hostId: string, home: FakeHome): string {
  const root: string | null = skillsRootFor(hostId, home.home);
  if (root === null) {
    throw new Error(`no skills root declared for ${hostId}`);
  }
  return relativeToHome(home, join(root, SKILL_DIRECTORY_NAME, "SKILL.md"));
}

describe("installing and uninstalling two hosts", () => {
  it("removes only the host that was uninstalled", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code", "codex"])).status).toBe(0);
      expect(await home.isFile(skillFile("claude-code", home))).toBe(true);
      expect(await home.isFile(skillFile("codex", home))).toBe(true);

      expect((await runInstaller(["uninstall", "claude-code"])).status).toBe(0);
      expect(await home.exists(skillFile("claude-code", home))).toBe(false);
      expect((await home.read(skillFile("codex", home))).length).toBeGreaterThan(0);
    });
  });

  it("gives every registered host a copy of its own", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      // DSH is wired through its own `dsh plugin` command, so it needs that
      // executable on PATH; every other host installs without one.
      const bin: string = await writeStandInDsh(home);
      const status: number = await withRestrictedPath([bin], async () =>
        (await runInstaller(["install", ...HOSTS.map((host: HostContribution) => host.id)])).status);
      expect(status).toBe(0);
      for (const host of HOSTS) {
        const path: string = skillFile(host.id, home);
        expect(await home.isFile(path), path).toBe(true);
      }
    });
  });
});

describe("the fixed child-directory name", () => {
  it("is the same name for every host", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const bin: string = await writeStandInDsh(home);
      const status: number = await withRestrictedPath([bin], async () =>
        (await runInstaller(["install", ...HOSTS.map((host: HostContribution) => host.id)])).status);
      expect(status).toBe(0);
      const children: string[][] = [];
      for (const host of HOSTS) {
        const root: string | null = skillsRootFor(host.id, home.home);
        expect(root).not.toBeNull();
        children.push(await home.list(relativeToHome(home, root as string)));
      }
      for (const listed of children) {
        expect(listed).toContain(SKILL_DIRECTORY_NAME);
      }
    });
  });

  it("keeps the other host's copy when one host is uninstalled", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code", "codebuddy"])).status).toBe(0);
      const claude: string = skillFile("claude-code", home);
      const buddy: string = skillFile("codebuddy", home);

      expect((await runInstaller(["uninstall", "claude-code"])).status).toBe(0);
      expect(await home.exists(claude)).toBe(false);
      expect((await home.read(buddy)).length).toBeGreaterThan(0);
    });
  });

  it("preserves a foreign sibling under the skills root", async () => {
    const source: string | null = packagedSkillDirectory();
    expect(source).not.toBeNull();
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      await copyPackagedSkill("claude-code", home.home);
      await home.write(".claude/skills/foreign/SKILL.md", "# foreign\n");

      const result = await removeInstalledSkill("claude-code", home.home);
      expect(result.ok).toBe(true);
      expect(await home.exists(skillFile("claude-code", home))).toBe(false);
      expect(await home.isFile(".claude/skills/foreign/SKILL.md")).toBe(true);
    });
  });
});

describe("a copy after the package moved", () => {
  it("refreshes the installed content in place", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await copyPackagedSkill("claude-code", home.home)).ok).toBe(true);
      const before: string = await home.read(".claude/skills/wenyuan-memory/SKILL.md");

      // Simulate a newer package by overwriting the installed copy, then a
      // re-install restores the packaged content.
      await home.write(".claude/skills/wenyuan-memory/SKILL.md", "# stale\n");
      await copyPackagedSkill("claude-code", home.home);
      const after: string = await home.read(".claude/skills/wenyuan-memory/SKILL.md");
      expect(after).toBe(before);
    });
  });

  it("refreshes a nested packaged file", async () => {
    const source: string | null = packagedSkillDirectory();
    expect(source).not.toBeNull();
    const nested: string = join(source as string, "guides", "usage.md");
    await mkdir(join(source as string, "guides"), { recursive: true });
    await writeFile(nested, "# usage\n");
    try {
      await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
        await copyPackagedSkill("claude-code", home.home);
        expect(await home.isFile(".claude/skills/wenyuan-memory/guides/usage.md")).toBe(true);

        expect((await removeInstalledSkill("claude-code", home.home)).ok).toBe(true);
        expect(await home.exists(".claude/skills/wenyuan-memory")).toBe(false);
        expect(await home.exists(".claude/skills")).toBe(true);
      });
    } finally {
      await removeFile(join(source as string, "guides"), { recursive: true, force: true });
    }
  });
});

/** The path of a directory relative to the fake home, for home.list(). */
function relativeToHome(home: FakeHome, absolute: string): string {
  return absolute.startsWith(home.home) ? absolute.slice(home.home.length + 1) : absolute;
}
