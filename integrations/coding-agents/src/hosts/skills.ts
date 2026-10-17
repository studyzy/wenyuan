import { existsSync } from "node:fs";
import { cp, mkdir, readdir, stat } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { rm } from "node:fs/promises";
import { installedSkillDirectoryFor, SKILL_DIRECTORY_NAME } from "./skill-locations";

/**
 * The packaged companion skill and its copy into each host.
 *
 * The destination is resolved through the shared skill-location declaration,
 * so the installer and the session-start refresh can never disagree about where
 * a host's skill lives. The copy is idempotent: it overwrites the fixed child
 * directory recursively and is a handled no-op when the package ships no built
 * skill file. Removal deletes exactly that fixed child directory and leaves
 * anything else under the skills root in place.
 */

export { SKILL_DIRECTORY_NAME };
export const PACKAGED_SKILL_DIRECTORY = "skill";
export const PACKAGED_SKILL_FILE = "SKILL.md";

export interface SkillResult {
  ok: boolean;
  changed: boolean;
  messages: string[];
}

/**
 * The packaged skill directory shipped with the package, or null when this
 * package location does not carry one. Nothing is invented in its place: a
 * missing skill is reported rather than copied from somewhere else.
 */
export function packagedSkillDirectory(
  from: string = dirname(fileURLToPath(import.meta.url)),
): string | null {
  let current: string = resolve(from);
  for (let depth = 0; depth < 6; depth += 1) {
    const candidate: string = join(current, PACKAGED_SKILL_DIRECTORY, PACKAGED_SKILL_FILE);
    if (existsSync(candidate)) {
      return join(current, PACKAGED_SKILL_DIRECTORY);
    }
    const parent: string = dirname(current);
    if (parent === current) {
      break;
    }
    current = parent;
  }
  return null;
}

/** The packaged skill file a host loads, or null when the package ships none. */
export function packagedSkillFile(): string | null {
  const directory: string | null = packagedSkillDirectory();
  return directory === null ? null : join(directory, PACKAGED_SKILL_FILE);
}

/**
 * Copies the packaged skill into one host's skills directory.
 *
 * Recipients pass the host and home rather than a path, so the destination is
 * always the shared declaration's answer. The copy creates a missing skills
 * root, overwrites the fixed child directory in place, and is a handled no-op
 * when the package ships no built skill file.
 */
export async function copyPackagedSkill(hostId: string, home: string): Promise<SkillResult> {
  const target: string | null = installedSkillDirectoryFor(hostId, home);
  if (target === null) {
    return {
      ok: true,
      changed: false,
      messages: [`${hostId} has no declared skills root, so no companion skill was placed.`],
    };
  }
  const source: string | null = packagedSkillDirectory();
  if (source === null) {
    return {
      ok: true,
      changed: false,
      messages: [
        `the packaged companion skill was not found beside this command, so none was copied into ${target}`,
      ],
    };
  }
  await mkdir(dirname(target), { recursive: true });
  await cp(source, target, { recursive: true, force: true });
  return { ok: true, changed: true, messages: [] };
}

/**
 * Removes the fixed child directory under a host's skills root, recursively and
 * tolerating its absence. Nothing else under the skills root is touched, so a
 * foreign skill sibling survives an uninstall.
 */
export async function removeInstalledSkill(hostId: string, home: string): Promise<SkillResult> {
  const target: string | null = installedSkillDirectoryFor(hostId, home);
  if (target === null) {
    return { ok: true, changed: false, messages: [] };
  }
  if (!(await isDirectory(target))) {
    return { ok: true, changed: false, messages: [] };
  }
  await rm(target, { recursive: true, force: true });
  return { ok: true, changed: true, messages: [] };
}

async function isDirectory(path: string): Promise<boolean> {
  try {
    return (await stat(path)).isDirectory();
  } catch {
    return false;
  }
}

/** Every file below a directory, for a caller that needs the packaged inventory. */
export async function packagedSkillFiles(): Promise<string[]> {
  const source: string | null = packagedSkillDirectory();
  return source === null ? [PACKAGED_SKILL_FILE] : listFiles(source);
}

async function listFiles(directory: string): Promise<string[]> {
  const files: string[] = [];
  let entries: { name: string; isDirectory(): boolean }[];
  try {
    entries = await readdir(directory, { withFileTypes: true });
  } catch {
    return files;
  }
  for (const entry of entries) {
    const path: string = join(directory, entry.name);
    if (entry.isDirectory()) {
      files.push(...(await listFiles(path)).map((nested) => join(entry.name, nested)));
    } else {
      files.push(entry.name);
    }
  }
  return files;
}