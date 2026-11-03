import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { resolveSkillDirs } from "../../../src/core/skill-dirs";
import { syncCompanionSkill } from "../../../src/core/skill-sync";
import { HOST_IDS } from "../../../src/hosts/registry";
import { installedSkillDirectoryFor, SKILL_DIRECTORY_NAME } from "../../../src/hosts/skill-locations";

describe("syncCompanionSkill", () => {
  const dirs: string[] = [];
  const tmp = (p: string) => {
    const d = mkdtempSync(join(tmpdir(), p));
    dirs.push(d);
    return d;
  };
  afterEach(() => {
    for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
  });

  const setup = (installedContent?: string) => {
    const home = tmp("wenyuan-skill-home-");
    const src = tmp("wenyuan-skill-src-");
    writeFileSync(join(src, "SKILL.md"), "NEW CONTENT v2");
    if (installedContent !== undefined) {
      const dst = installedSkillDirectoryFor("claude-code", home)!;
      mkdirSync(dst, { recursive: true });
      writeFileSync(join(dst, "SKILL.md"), installedContent);
    }
    return { home, src };
  };

  it("updates a stale installed copy to the packaged content", () => {
    const { home, src } = setup("OLD CONTENT v1");
    syncCompanionSkill("claude-code", { home, srcDir: src });
    expect(
      readFileSync(join(installedSkillDirectoryFor("claude-code", home)!, "SKILL.md"), "utf8")
    ).toBe("NEW CONTENT v2");
  });

  it("does NOT install where the skill was never installed (uninstall stays respected)", () => {
    const { home, src } = setup(undefined);
    syncCompanionSkill("claude-code", { home, srcDir: src });
    expect(existsSync(installedSkillDirectoryFor("claude-code", home)!)).toBe(false);
  });

  it("installs where nothing is installed when the caller asks (plugin-manager route)", () => {
    // A host whose plugin is wired by its own plugin manager never runs our
    // installer, so the persistent-plugin hosts pass `install` at seed time.
    // The plugin being loaded is the installation signal there.
    const { home, src } = setup(undefined);
    syncCompanionSkill("dsh", { home, srcDir: src, install: true });
    expect(
      readFileSync(join(installedSkillDirectoryFor("dsh", home)!, "SKILL.md"), "utf8")
    ).toBe("NEW CONTENT v2");
  });

  it("no-ops on identical content, on a missing packaged skill, and on hosts without a skills root", () => {
    const { home, src } = setup("NEW CONTENT v2");
    syncCompanionSkill("claude-code", { home, srcDir: src }); // same content — no throw, unchanged
    syncCompanionSkill("opencode", { home, srcDir: src }); // no declared skills root — no-op
    expect(
      readFileSync(join(installedSkillDirectoryFor("claude-code", home)!, "SKILL.md"), "utf8")
    ).toBe("NEW CONTENT v2");

    const empty = tmp("wenyuan-skill-none-");
    writeFileSync(join(empty, "PLACEHOLDER"), "x");
    syncCompanionSkill("claude-code", { home, srcDir: empty }); // no packaged SKILL.md — no-op
    expect(
      readFileSync(join(installedSkillDirectoryFor("claude-code", home)!, "SKILL.md"), "utf8")
    ).toBe("NEW CONTENT v2");
  });
});

/**
 * Family-wide guard: a host whose installer copies the skill but whose skills
 * root the session-start sync cannot resolve would install once and then never
 * refresh — the package upgrades while that host keeps the old SKILL.md until
 * someone re-installs. A per-host test cannot catch this, because the host that
 * forgets is by definition the host whose test nobody wrote. The sync derives
 * its destinations from the installer's own declaration, so the two directions
 * below pin that derivation over the WHOLE registered family.
 */
describe("skill locations cover the whole host family", () => {
  const home = join("/home", "probe-user"); // never touched: the derivation is pure path math

  it("resolves a skills root for every registered host that installs the skill", () => {
    // If this list is empty the check has stopped checking anything — the
    // registry was renamed.
    expect(HOST_IDS.length).toBeGreaterThan(0);
    // Devin and Dcode have no skills mechanism of their own: the installer
    // places no companion skill for them, so no root to refresh exists and
    // the sync leaves them alone, exactly as for an unregistered host.
    const NO_SKILL_HOSTS = ["devin-cli", "dcode"];
    const unresolvable = HOST_IDS.filter(
      (id) => !NO_SKILL_HOSTS.includes(id) && resolveSkillDirs(id, home) === undefined,
    );
    expect(unresolvable).toEqual([]);
  });

  it("sends the sync to exactly where the installer places the skill", () => {
    for (const id of HOST_IDS) {
      const parts = resolveSkillDirs(id, home);
      if (parts === undefined) {
        // Devin and Dcode place no skill, so there is nothing to agree on.
        expect(installedSkillDirectoryFor(id, home)).toBeNull();
        continue;
      }
      expect(join(home, ...parts, SKILL_DIRECTORY_NAME)).toBe(
        installedSkillDirectoryFor(id, home)
      );
    }
  });

  it("gives an unregistered host no directory to write", () => {
    // A stale entry would be a path this module keeps refreshing for a host the
    // installer no longer supports; the derivation cannot have one.
    expect(resolveSkillDirs("opencode", home)).toBeUndefined();
    expect(resolveSkillDirs("", home)).toBeUndefined();
  });
});
