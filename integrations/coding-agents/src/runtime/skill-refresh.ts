import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { copyPackagedSkill, packagedSkillDirectory } from "../hosts/skills";
import { installedSkillDirectoryFor } from "../hosts/skill-locations";
import { PACKAGED_SKILL_FILE } from "../hosts/skills";

/**
 * The per-session companion-skill refresh.
 *
 * At session start, a host whose installed skill has drifted from the packaged
 * copy is re-copied in place, so upgrading the package upgrades the installed
 * skill without a re-install. The refresh is presence-gated: a host the skill
 * was never installed for, or was uninstalled from, is left alone and never
 * silently regains one. It is best-effort and returns nothing; a caller wraps
 * it so a failure while comparing or copying cannot break a session.
 */

export interface SkillRefreshOutcome {
  /** True when the installed copy was replaced in place. */
  refreshed: boolean;
}

/**
 * Re-copies a host's installed skill when it differs from the packaged copy.
 * A host with no installed skill directory is left untouched. A failure while
 * reading or copying is swallowed, so the session is unaffected.
 */
export async function refreshInstalledSkill(hostId: string, home: string): Promise<SkillRefreshOutcome> {
  try {
    const installed: string | null = installedSkillDirectoryFor(hostId, home);
    if (installed === null) {
      return { refreshed: false };
    }
    const source: string | null = packagedSkillDirectory();
    if (source === null) {
      return { refreshed: false };
    }
    const installedFile: string | null = await readIfPresent(join(installed, PACKAGED_SKILL_FILE));
    if (installedFile === null) {
      // Presence gate: no installed skill, so nothing is refreshed or installed.
      return { refreshed: false };
    }
    const packagedFile: string | null = await readIfPresent(join(source, PACKAGED_SKILL_FILE));
    if (packagedFile === installedFile) {
      return { refreshed: false };
    }
    const result = await copyPackagedSkill(hostId, home);
    return { refreshed: result.changed };
  } catch {
    return { refreshed: false };
  }
}

async function readIfPresent(path: string): Promise<string | null> {
  try {
    return await readFile(path, "utf8");
  } catch {
    return null;
  }
}