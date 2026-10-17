import { describe, expect, it } from "vitest";
import { join } from "node:path";
import {
  installedSkillDirectoryFor,
  SKILL_DIRECTORY_NAME,
  skillsRootFor,
} from "../../src/hosts/skill-locations";

/**
 * The one shared skills-root map. Every approved host resolves a non-empty root
 * from the map, the installed directory is the root joined with the one fixed
 * child-directory name (no host component), and a host outside the map resolves
 * to nothing so its placement path is never reached.
 */

const APPROVED: readonly string[] = ["claude-code", "codex", "codebuddy"];

describe("the skills-root map", () => {
  it.each(APPROVED)("resolves a non-empty root for %s", (hostId: string) => {
    const root: string | null = skillsRootFor(hostId, "/home/u");
    expect(root).not.toBeNull();
    expect((root as string).length).toBeGreaterThan(0);
  });

  it("resolves the second host to the shared agentskills-standard root", () => {
    expect(skillsRootFor("codex", "/home/u")).toBe(join("/home/u", ".agents/skills"));
  });

  it("resolves each host to its declared dot-directory skills child", () => {
    expect(skillsRootFor("claude-code", "/home/u")).toBe(join("/home/u", ".claude/skills"));
    expect(skillsRootFor("codebuddy", "/home/u")).toBe(join("/home/u", ".codebuddy/skills"));
  });

  it("resolves nothing for a host outside the map", () => {
    expect(skillsRootFor("not-a-host", "/home/u")).toBeNull();
    expect(installedSkillDirectoryFor("not-a-host", "/home/u")).toBeNull();
  });
});

describe("the installed skill directory", () => {
  it.each(APPROVED)("derives %s's directory from the root and the one fixed name", (hostId: string) => {
    const root: string | null = skillsRootFor(hostId, "/home/u");
    expect(installedSkillDirectoryFor(hostId, "/home/u")).toBe(
      join(root as string, SKILL_DIRECTORY_NAME),
    );
  });

  it("adds no host-identifier component", () => {
    const directory: string | null = installedSkillDirectoryFor("claude-code", "/home/u");
    expect(directory).toBe(join("/home/u", ".claude/skills", SKILL_DIRECTORY_NAME));
    expect(directory).not.toContain("/claude-code/");
  });

  it("uses the same fixed name for two hosts with different roots", () => {
    const first: string | null = installedSkillDirectoryFor("claude-code", "/home/u");
    const second: string | null = installedSkillDirectoryFor("codebuddy", "/home/u");
    expect(first?.endsWith(SKILL_DIRECTORY_NAME)).toBe(true);
    expect(second?.endsWith(SKILL_DIRECTORY_NAME)).toBe(true);
  });
});