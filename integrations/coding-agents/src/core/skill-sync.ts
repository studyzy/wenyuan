/**
 * Companion-skill self-update: upgrading the package refreshes its own skill/
 * directory, but the copies placed in each host's skills directory would go
 * stale. Every session start compares the installed copy against the packaged
 * one and re-copies on drift — so upgrading the plugin upgrades the skill,
 * with no re-install needed.
 *
 * Presence-gated by default: a host where the skill was never installed (or was
 * uninstalled) is left alone, because the hook registration and the skill are
 * installed by the same command, so an absent skill is a decision.
 *
 * `install: true` flips that, and is what the persistent-plugin hosts pass:
 * those can be wired by the host's own plugin manager — a route the installer
 * never sees — so the plugin being loaded is the installation signal. A
 * running plugin can only mean the host still wants the skill.
 *
 * Best-effort by contract: skill freshness must never break a session.
 */
import { cpSync, existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { homeDirectory } from "../config/paths";
import { PACKAGED_SKILL_FILE, packagedSkillDirectory } from "../hosts/skills";
import { SKILL_DIRECTORY_NAME, resolveSkillDirs } from "./skill-dirs";

export function syncCompanionSkill(
  hostId: string,
  opts: { home?: string; srcDir?: string; install?: boolean } = {}
): void {
  try {
    const home = opts.home ?? homeDirectory();
    const parts = resolveSkillDirs(hostId, home);
    if (!parts) return; // host without a skills directory — nothing to refresh
    const src = opts.srcDir ?? packagedSkillDirectory() ?? undefined;
    if (!src) return; // no packaged skill beside this build (a dev tree before the skill build)
    const srcMd = join(src, PACKAGED_SKILL_FILE);
    if (!existsSync(srcMd)) return;
    const dst = join(home, ...parts, SKILL_DIRECTORY_NAME);
    const dstMd = join(dst, PACKAGED_SKILL_FILE);
    const installed = existsSync(dstMd);
    if (!installed && !opts.install) return; // never installed here — not ours to decide
    if (!installed || readFileSync(srcMd, "utf8") !== readFileSync(dstMd, "utf8")) {
      cpSync(src, dst, { recursive: true }); // recursive cpSync creates the skills root too
    }
  } catch {
    /* best-effort — skill freshness must never break a session */
  }
}
