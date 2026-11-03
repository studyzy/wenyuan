import { describe, expect, it } from "vitest";
import { join } from "node:path";
import {
  installedSkillDirectoryFor,
  SKILL_DIRECTORY_NAME,
  skillsRootFor,
  traecodeDotDirectory,
} from "../../src/hosts/skill-locations";

/**
 * The one shared skills-root map. Every approved host resolves a non-empty root
 * from the map, the installed directory is the root joined with the one fixed
 * child-directory name (no host component), and a host outside the map resolves
 * to nothing so its placement path is never reached.
 */

const APPROVED: readonly string[] = [
  "claude-code",
  "codex",
  "codebuddy",
  "workbuddy",
  "dsh",
  "antigravity-cli",
  "grok-build",
  "qwen-code",
  "factory-droid",
  "zcode",
  "kimi",
];

/** Hosts with no skills mechanism, which resolve no root at all. */
const WITHOUT_ROOTS: readonly string[] = ["devin-cli", "dcode"];

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
    expect(skillsRootFor("workbuddy", "/home/u")).toBe(join("/home/u", ".workbuddy/skills"));
    expect(skillsRootFor("dsh", "/home/u")).toBe(join("/home/u", ".dsh/skills"));
    expect(skillsRootFor("antigravity-cli", "/home/u")).toBe(join("/home/u", ".gemini/config/skills"));
    expect(skillsRootFor("grok-build", "/home/u")).toBe(join("/home/u", ".grok/skills"));
    expect(skillsRootFor("qwen-code", "/home/u")).toBe(join("/home/u", ".qwen/skills"));
    expect(skillsRootFor("factory-droid", "/home/u")).toBe(join("/home/u", ".factory/skills"));
    expect(skillsRootFor("zcode", "/home/u")).toBe(join("/home/u", ".zcode/skills"));
    expect(skillsRootFor("kimi", "/home/u")).toBe(join("/home/u", ".kimi-code/skills"));
  });

  it("resolves TraeCode through the edition probe, CN default first", () => {
    expect(skillsRootFor("traecode", "/home/u")).toBe(join("/home/u", ".trae-cn/skills"));
    expect(traecodeDotDirectory("/home/u")).toBe(".trae-cn");
  });

  it("resolves nothing for the hosts without a skills mechanism", () => {
    for (const hostId of WITHOUT_ROOTS) {
      expect(skillsRootFor(hostId, "/home/u")).toBeNull();
      expect(installedSkillDirectoryFor(hostId, "/home/u")).toBeNull();
    }
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