import { existsSync } from "node:fs";
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
 * agentskills-standard root; the others keep their own dot-directory.
 */
const SKILLS_ROOTS: Readonly<Record<string, string>> = {
  "claude-code": ".claude/skills",
  codex: ".agents/skills",
  codebuddy: ".codebuddy/skills",
  workbuddy: ".workbuddy/skills",
  "cursor-cli": ".cursor/skills",
  "copilot-cli": ".copilot/skills",
  dsh: ".dsh/skills",
  "antigravity-cli": ".gemini/config/skills",
  "grok-build": ".grok/skills",
  "qwen-code": ".qwen/skills",
  "factory-droid": ".factory/skills",
  zcode: ".zcode/skills",
  kimi: ".kimi-code/skills",
  // TraeCode's root follows the installed edition's dot-directory; resolved
  // per home by traecodeSkillsRootFor below, so it is absent from this map.
};

/**
 * The TRAE editions' user-level dot-directories, most specific probe first: the
 * CN build uses ~/.trae-cn and the international build ~/.trae. The app creates
 * the directory itself, so whichever exists names the edition; with neither
 * present the CN name is the default.
 */
const TRAE_DOT_DIRECTORIES: readonly string[] = [".trae-cn", ".trae"];

/**
 * The edition's dot-directory name under home, kept relative so both the hooks
 * config path and the skills root resolve through this one probe and can never
 * name different editions.
 */
export function traecodeDotDirectory(home: string): string {
  for (const directory of TRAE_DOT_DIRECTORIES) {
    if (existsSync(join(home, directory))) {
      return directory;
    }
  }
  return ".trae-cn";
}

/** TraeCode's skills root, resolved through the edition probe. */
export function traecodeSkillsRootFor(home: string): string {
  return join(home, traecodeDotDirectory(home), "skills");
}

/**
 * The host's skills root, or null when the host has no declared root. A null
 * root means the skill placement path is not reached for that host.
 */
export function skillsRootFor(hostId: string, home: string): string | null {
  if (hostId === "traecode") {
    return traecodeSkillsRootFor(home);
  }
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