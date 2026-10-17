import { describe, expect, it } from "vitest";
import { mkdir, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { copyPackagedSkill, packagedSkillDirectory } from "../../src/hosts/skills";
import { refreshInstalledSkill } from "../../src/runtime/skill-refresh";
import { withFakeHome, type FakeHome } from "../helpers/fake-home";

/**
 * The per-session refresh is presence-gated and drift-only: an installed copy
 * that differs is replaced in place, a matching copy and a host with no
 * installed skill are left untouched, and a failure while comparing or copying
 * is swallowed so the session is unaffected.
 */

const INSTALLED = ".claude/skills/wenyuan-memory/SKILL.md";

describe("refreshing an installed skill", () => {
  it("replaces a drifted copy in place", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      await copyPackagedSkill("claude-code", home.home);
      await home.write(INSTALLED, "# stale\n");

      const outcome = await refreshInstalledSkill("claude-code", home.home);
      expect(outcome.refreshed).toBe(true);
      expect(await home.read(INSTALLED)).not.toBe("# stale\n");
    });
  });

  it("leaves a matching copy untouched", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      await copyPackagedSkill("claude-code", home.home);
      const before: string = await home.read(INSTALLED);

      const outcome = await refreshInstalledSkill("claude-code", home.home);
      expect(outcome.refreshed).toBe(false);
      expect(await home.read(INSTALLED)).toBe(before);
    });
  });

  it("gives a host with no installed skill none", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const outcome = await refreshInstalledSkill("claude-code", home.home);
      expect(outcome.refreshed).toBe(false);
      expect(await home.exists(".claude/skills/wenyuan-memory")).toBe(false);
    });
  });

  it("does nothing for a host outside the map", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const outcome = await refreshInstalledSkill("not-a-host", home.home);
      expect(outcome.refreshed).toBe(false);
    });
  });

  it("swallows a failure while comparing", async () => {
    const source: string | null = packagedSkillDirectory();
    expect(source).not.toBeNull();
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      await copyPackagedSkill("claude-code", home.home);
      // Make the installed skill file a directory so reading it as text fails.
      const installedFile: string = join(home.home, INSTALLED);
      await rm(installedFile, { force: true });
      await mkdir(installedFile, { recursive: true });
      try {
        const outcome = await refreshInstalledSkill("claude-code", home.home);
        expect(outcome.refreshed).toBe(false);
      } finally {
        await rm(installedFile, { recursive: true, force: true });
      }
    });
  });
});

describe("refreshing with no packaged skill", () => {
  it("is a no-op when the package ships no built skill", async () => {
    const source: string | null = packagedSkillDirectory();
    expect(source).not.toBeNull();
    const skillFile: string = join(source as string, "SKILL.md");
    const saved: string = await import("node:fs/promises").then((fs) => fs.readFile(skillFile, "utf8"));
    await rm(skillFile, { force: true });
    try {
      await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
        await mkdir(join(home.home, ".claude/skills/wenyuan-memory"), { recursive: true });
        const outcome = await refreshInstalledSkill("claude-code", home.home);
        expect(outcome.refreshed).toBe(false);
      });
    } finally {
      await writeFile(skillFile, saved, "utf8");
    }
  });
});