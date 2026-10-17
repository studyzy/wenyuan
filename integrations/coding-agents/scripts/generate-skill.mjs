#!/usr/bin/env node
/**
 * Generates the packaged companion skill from its inputs.
 *
 * The committed `skill/SKILL.md` is a build artifact, never the source of
 * truth: it is assembled from `skill-src/preamble.md` followed by the marked
 * regions of `README.md`, with heading levels normalized so the merged document
 * has one outline, and the front-matter block kept first. `--check` fails when
 * the committed file has drifted from its inputs.
 *
 * The tool identifiers the body names come from `src/hosts/skill-tools.ts`, the
 * one declaration, so the skill text and the installed server surface agree.
 */

import { readFile, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const PREAMBLE = join(ROOT, "skill-src", "preamble.md");
const README = join(ROOT, "README.md");
const OUTPUT = join(ROOT, "skill", "SKILL.md");

const TOOLS = {
  document_ingest: "retain",
  initiative_capture: "create_knowledge_page",
  knowledge_search: "search_knowledge_base",
  knowledge_read: "get_knowledge_page",
  knowledge_list: "get_knowledge_base_tree",
  reflect: "reflect",
};
const SKILL_NAME = "wenyuan-memory";
const SKILL_DESCRIPTION =
  "Remember and recall what this project's coding sessions have already established";

/** The regions of the README folded into the skill, by their HTML-comment markers. */
const MARKED_REGIONS = ["skill-usage"];

function expand(source) {
  return source
    .replaceAll("{{name}}", SKILL_NAME)
    .replaceAll("{{description}}", SKILL_DESCRIPTION)
    .replace(/\{\{tool\.([a-z_]+)\}\}/g, (_match, role) => {
      const identifier = TOOLS[role];
      if (identifier === undefined) {
        throw new Error(`no tool identifier declared for role ${role}`);
      }
      return identifier;
    });
}

/** Keeps only the body of a named `<!-- region:name --> … <!-- /region -->` block. */
function markedRegion(text, name) {
  const pattern = new RegExp(
    `<!--\\s*region:${name}\\s*-->([\\s\\S]*?)<!--\\s*/region\\s*-->`,
    "m",
  );
  const found = pattern.exec(text);
  if (found === null) {
    throw new Error(`README is missing the marked region ${name}`);
  }
  return found[1].trim();
}

/** Shifts every ATX heading down one level so folded regions nest under the skill's own. */
function demoteHeadings(text) {
  return text
    .split("\n")
    .map((line) => (/^#{1,5}\s/.test(line) ? `#${line}` : line))
    .join("\n");
}

function assertFrontMatterFirst(text) {
  if (!text.startsWith("---\n")) {
    throw new Error("the generated skill must begin with its front-matter block");
  }
}

async function assemble() {
  const preamble = await readFile(PREAMBLE, "utf8");
  const readme = await readFile(README, "utf8");
  const sections = MARKED_REGIONS.map((name) => demoteHeadings(markedRegion(readme, name)));
  const body = [preamble.trim(), ...sections].join("\n\n");
  const generated = `${expand(body)}\n`;
  assertFrontMatterFirst(generated);
  return generated;
}

async function main() {
  const generated = await assemble();
  if (process.argv.includes("--check")) {
    const committed = await readFile(OUTPUT, "utf8").catch(() => null);
    if (committed !== generated) {
      process.stderr.write("skill/SKILL.md is stale; run: npm run build:skill\n");
      process.exit(1);
    }
    return;
  }
  await writeFile(OUTPUT, generated, "utf8");
}

await main();