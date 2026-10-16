import { existsSync } from "node:fs";
import { cp, mkdir, readdir, rm, stat } from "node:fs/promises";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { OWNERSHIP_MARKER } from "../write/marker";

/**
 * The packaged companion skill and its copy into each host.
 *
 * Hosts may resolve their skills directory to a root another host also uses,
 * so each host gets a directory of its own inside the shared fixed name:
 * uninstalling one host then cannot take another host's still-live copy, and a
 * skills directory holding anything this package did not create is never
 * deleted outright.
 */

export const SKILL_DIRECTORY_NAME = OWNERSHIP_MARKER;
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

/** Where this host's own copy of the skill lives. */
export function installedSkillDirectory(skillsRoot: string, hostId: string): string {
  return join(skillsRoot, SKILL_DIRECTORY_NAME, hostId);
}

/** Copies the packaged skill into one host's skills directory. */
export async function copyPackagedSkill(skillsRoot: string, hostId: string): Promise<SkillResult> {
  const source: string | null = packagedSkillDirectory();
  if (source === null) {
    return {
      ok: false,
      changed: false,
      messages: [
        `the packaged companion skill was not found beside this command, so it was not copied into ${installedSkillDirectory(skillsRoot, hostId)}`,
      ],
    };
  }
  const target: string = installedSkillDirectory(skillsRoot, hostId);
  await mkdir(target, { recursive: true });
  await cp(source, target, { recursive: true });
  return { ok: true, changed: true, messages: [] };
}

/**
 * Removes one host's own copy, and only the files this package put there. A
 * directory that still holds anything foreign is left in place.
 */
export async function removeInstalledSkill(
  skillsRoot: string,
  hostId: string,
): Promise<SkillResult> {
  const target: string = installedSkillDirectory(skillsRoot, hostId);
  if (!(await isDirectory(target))) {
    return { ok: true, changed: false, messages: [] };
  }
  for (const file of await packagedSkillFiles()) {
    await rm(join(target, file), { force: true });
  }
  if ((await listFiles(target)).length > 0) {
    return {
      ok: true,
      changed: true,
      messages: [
        `${target} also holds files this package did not create, so the directory was kept; only the packaged skill files were removed.`,
      ],
    };
  }
  await rm(target, { recursive: true, force: true });
  const shared: string = dirname(target);
  if ((await listFiles(shared)).length === 0) {
    await rm(shared, { recursive: true, force: true });
  }
  return { ok: true, changed: true, messages: [] };
}

/**
 * The files the packaged skill carries. Removal goes by this inventory rather
 * than by whatever the package happens to hold at uninstall time, so a copy is
 * still removed after the package that installed it was replaced or moved.
 */
async function packagedSkillFiles(): Promise<string[]> {
  const source: string | null = packagedSkillDirectory();
  return source === null ? [PACKAGED_SKILL_FILE] : listFiles(source);
}

async function isDirectory(path: string): Promise<boolean> {
  try {
    return (await stat(path)).isDirectory();
  } catch {
    return false;
  }
}

/**
 * Every file below a directory, named relative to that directory rather than
 * by bare basename, so a nested file is removed from where it actually sits.
 */
async function listFiles(directory: string): Promise<string[]> {
  const files: string[] = [];
  async function walk(current: string): Promise<void> {
    let entries: { name: string; isDirectory(): boolean }[];
    try {
      entries = await readdir(current, { withFileTypes: true });
    } catch {
      return;
    }
    for (const entry of entries) {
      const path: string = join(current, entry.name);
      if (entry.isDirectory()) {
        await walk(path);
      } else {
        files.push(relative(directory, path));
      }
    }
  }
  await walk(directory);
  return files;
}
