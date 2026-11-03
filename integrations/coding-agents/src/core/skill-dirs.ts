/**
 * Where each host keeps the companion skill, as home-relative path parts.
 *
 * Two independent code paths write and refresh those directories: the installer
 * copies the packaged skill in and removes it again, and every session start
 * re-copies it on drift so upgrading the package upgrades the skill too. They
 * must never hold the answer separately — the copy that holds its own list is
 * the copy that forgets a host the other one knows — so this module derives
 * every location from the installer's own declaration
 * (src/hosts/skill-locations.ts) instead of naming the roots a second time.
 *
 * A host outside that declaration resolves to no directory at all: it has no
 * skills mechanism this package knows about, and the session-start sync leaves
 * it alone.
 */
import { dirname, isAbsolute, relative, sep } from "node:path";
import { installedSkillDirectoryFor, SKILL_DIRECTORY_NAME } from "../hosts/skill-locations";

export { SKILL_DIRECTORY_NAME };

/**
 * The host's skills ROOT, split into home-relative parts (the fixed child
 * directory every host installs under is NOT included — callers append
 * SKILL_DIRECTORY_NAME), or undefined when the host has no declared skills
 * directory. Derived rather than re-declared, so the session-start refresh and
 * the installer cannot disagree about a root.
 */
export function resolveSkillDirs(hostId: string, home: string): string[] | undefined {
  const installed: string | null = installedSkillDirectoryFor(hostId, home);
  if (installed === null) {
    return undefined;
  }
  const relativePath: string = relative(home, dirname(installed));
  if (relativePath.length === 0 || relativePath.startsWith("..") || isAbsolute(relativePath)) {
    return undefined;
  }
  return relativePath.split(sep);
}
