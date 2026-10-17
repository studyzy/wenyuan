import { join } from "node:path";
import { OWNERSHIP_MARKER } from "../write/marker";

/**
 * Where each host's companion skill lives, declared once.
 *
 * Two independent paths read this module: the installer that places and removes
 * a host's skill, and the per-session refresh that re-copies it on drift. Both
 * resolve the same directory from the one map below, so placement and removal
 * can never disagree about a host's skills root.
 *
 * A skills root is home-relative. A host that can scan a shared root in
 * addition to its own still gets a directory of its own inside that root,
 * because removal is by the fixed child-directory name and a shared root would
 * otherwise let one host's uninstall take another host's live copy.
 */

/**
 * The one child-directory name every host installs its skill under, inside its
 * own skills root. Derived from the ownership marker so the installed surface
 * carries the same fixed spelling every other written path does.
 */
export const SKILL_DIRECTORY_NAME = OWNERSHIP_MARKER;

/**
 * The home-relative skills root for each approved host. Kept table-shaped
 * rather than branched on the host id, so adding a host is a data edit and a
 * host outside the map stays absent. The second host reads the shared
 * agentskills-standard root; the first and third keep their own dot-directory.
 */
const SKILLS_ROOTS: Readonly<Record<string, string>> = {
  "claude-code": ".claude/skills",
  codex: ".agents/skills",
  codebuddy: ".codebuddy/skills",
};

/**
 * The host's skills root, or null when the host has no declared root. A null
 * root means the skill placement path is not reached for that host.
 */
export function skillsRootFor(hostId: string, home: string): string | null {
  const relative: string | undefined = SKILLS_ROOTS[hostId];
  return relative === undefined ? null : join(home, relative);
}

/**
 * The directory a host's skill is installed into: its skills root joined with
 * the one fixed child-directory name, with no host-identifier component, so the
 * name is the same for every host.
 */
export function installedSkillDirectoryFor(hostId: string, home: string): string | null {
  const root: string | null = skillsRootFor(hostId, home);
  return root === null ? null : join(root, SKILL_DIRECTORY_NAME);
}