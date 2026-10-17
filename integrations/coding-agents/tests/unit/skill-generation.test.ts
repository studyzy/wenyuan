import { describe, expect, it } from "vitest";
import { readFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { SKILL_DESCRIPTION, SKILL_NAME, SKILL_TOOL_IDENTIFIERS } from "../../src/hosts/skill-tools";

/**
 * The packaged skill is a generated artifact: its front matter is first, its
 * body carries the four behavior sections, and it names exactly the tool
 * identifiers the one declaration pins. The generator's --check mode fails when
 * the committed file has drifted from its inputs.
 */

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");
const SKILL_FILE = join(ROOT, "skill", "SKILL.md");

async function installableSkillFile(): Promise<string> {
  return readFile(SKILL_FILE, "utf8");
}

describe("the generated skill file", () => {
  it("begins with the front-matter block, first", async () => {
    const text: string = await installableSkillFile();
    expect(text.startsWith("---\n")).toBe(true);
    const frontMatter: string = text.slice(4, text.indexOf("\n---", 4));
    expect(frontMatter).toContain(`name: ${SKILL_NAME}`);
    expect(frontMatter).toContain(`description: ${SKILL_DESCRIPTION}`);
  });

  it("describes the four behavior sections", async () => {
    const text: string = await installableSkillFile();
    expect(text).toContain("## What happens automatically");
    expect(text).toContain("## When the user asks you to remember something");
    expect(text).toContain("## When you need to retrieve something");
    expect(text).toContain("## When you find memory that is stale");
  });

  it("names every tool identifier from the one declaration", async () => {
    const text: string = await installableSkillFile();
    for (const identifier of Object.values(SKILL_TOOL_IDENTIFIERS)) {
      expect(text, identifier).toContain(identifier);
    }
  });

  it("carries no credential or connection value", async () => {
    const text: string = await installableSkillFile();
    expect(text).not.toMatch(/hsk_/);
    expect(text).not.toMatch(/apiToken/);
    expect(text).not.toMatch(/bearer/i);
  });

  it("is up to date with its inputs", () => {
    expect(existsSync(join(ROOT, "scripts", "generate-skill.mjs"))).toBe(true);
    expect(() =>
      execFileSync(process.execPath, [join(ROOT, "scripts", "generate-skill.mjs"), "--check"], {
        stdio: "pipe",
      }),
    ).not.toThrow();
  });
});