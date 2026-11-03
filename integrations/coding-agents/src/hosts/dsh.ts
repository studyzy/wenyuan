import { existsSync, readdirSync } from "node:fs";
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { registerMcpServer, removeMcpServer } from "../mcp/register";
import { MCP_SERVER_FILE_NAME } from "../runtime/layout";
import { copyPackagedSkill, removeInstalledSkill } from "./skills";
import type {
  HostContribution,
  HostEnvironment,
  HostInstallOptions,
  HostOperationResult,
} from "./types";

/**
 * DSH: a host whose memory wiring is a Cordis plugin, so this installer writes
 * the plugin row itself.
 *
 * DSH composes its behaviour from plugins, and its Claude Code / Codex hook
 * bridges are optional packages that lose the transcript, the session id and
 * the awaited stop boundary — so WenYuan reaches a DSH session through the
 * native Cordis plugin this package ships (src/plugins/dsh.ts, bundled to
 * dist/plugins/dsh.mjs): recall on agent/session-start and agent/pre-step,
 * write-back on agent/turn-stopping, and the wenyuan_* knowledge tools, all
 * in-process.
 *
 * The row is written into the HOME-level patch layer ($DSH_HOME/cordis.patch.yml),
 * which every profile composes after its bundles. The alternative — dsh plugin
 * add of a published package into ONE profile — needs pnpm, a network, and a
 * repeat per profile; patching the home layer loads the dist entry that is
 * already on this machine and always works. This package therefore writes NO
 * hook config for DSH: a hook beside the plugin would recall twice per prompt
 * and retain the transcript twice.
 *
 * The row's name MUST be a file:// URL. Cordis resolves it as an ES module
 * specifier, and a bare absolute path is not one — the plugin would be
 * silently skipped.
 */

/** The MCP server map DSH reads. */
export const DSH_MCP_CONFIG = ".dsh/mcp.json";

/** $DSH_HOME, as a path relative to the home directory. */
export const DSH_HOME_DIRECTORY = ".dsh";

/** The home-level patch layer every profile composes. */
export const DSH_PATCH_FILE = "cordis.patch.yml";

/** Marker comments bounding the block this installer owns in the patch file. */
export const DSH_BLOCK_START = "# WENYUAN_CODING_AGENTS_START";
export const DSH_BLOCK_END = "# WENYUAN_CODING_AGENTS_END";

/**
 * DSH requires the patch file to parse to a top-level ARRAY and fails boot on
 * anything else, so removing the last block must leave an empty list behind.
 * install treats this placeholder as not-user-content.
 */
export const DSH_EMPTY_LIST = "[]";

function dshPatchPath(home: string): string {
  return join(home, DSH_HOME_DIRECTORY, DSH_PATCH_FILE);
}

/** Everything in the patch file that is not our block, as install should leave it. */
function userPatches(existing: string): string {
  const pattern = new RegExp(DSH_BLOCK_START + "[\\s\\S]*?" + DSH_BLOCK_END + "\\n?", "g");
  return existing.replace(pattern, "").trim();
}

function profileNames(home: string): string[] {
  const directory = join(home, DSH_HOME_DIRECTORY, "profiles");
  if (!existsSync(directory)) return [];
  try {
    return readdirSync(directory);
  } catch {
    return [];
  }
}

/** Best-effort pointer at profile-level rows left by the pre-port installer. */
function removeLegacyProfileRows(env: HostEnvironment, messages: string[]): void {
  for (const name of profileNames(env.home)) {
    messages.push(
      "profile " + name + ": if it still lists @studyzy/dsh-wenyuan-memory, remove it with" +
        " dsh plugin --profile " + name + " remove @studyzy/dsh-wenyuan-memory",
    );
  }
}

const host: HostContribution = {
  id: "dsh",
  displayName: "DSH",
  detection: { executable: "dsh", configDirectory: ".dsh" },
  locations: {
    // No hook config: the plugin owns DSH's memory wiring, so this package
    // writes none rather than naming a file it would have to stop writing.
    configFiles: [],
    configFormat: "json",
    mcpConfigFiles: [DSH_MCP_CONFIG],
    mcpFormat: "json",
    skillsDirectory: ".dsh/skills",
  },

  async install(
    env: HostEnvironment,
    options: HostInstallOptions,
  ): Promise<HostOperationResult> {
    const messages: string[] = [];
    removeLegacyProfileRows(env, messages);

    // REPLACE any previous block rather than skipping: a re-install after the
    // package moved must repair the now-dead path, which is what install is for.
    const path = dshPatchPath(env.home);
    const existing = existsSync(path) ? readFileSync(path, "utf8") : "";
    const others = userPatches(existing);
    const entry = pathToFileURL(join(env.runtimeDirectory, "plugins", "dsh.mjs")).href;
    const block =
      DSH_BLOCK_START + "\n" +
      "- insert:\n" +
      "    - id: wenyuan\n" +
      "      name: " + JSON.stringify(entry) + "\n" +
      DSH_BLOCK_END + "\n";
    if (existsSync(path) && !existsSync(path + ".wenyuan-backup")) {
      copyFileSync(path, path + ".wenyuan-backup");
    }
    mkdirSync(join(env.home, DSH_HOME_DIRECTORY), { recursive: true });
    writeFileSync(path, others ? others + "\n\n" + block : block);
    messages.push(
      "dsh: plugin registered in " + path +
        " (applies to every dsh profile; start new sessions — already-open ones would gain the tools mid-conversation)",
    );

    const mcp = await registerMcpServer({
      hostId: host.id,
      home: env.home,
      serverPath: join(env.runtimeDirectory, MCP_SERVER_FILE_NAME),
      environment: options.environment,
    });
    messages.push(...mcp.messages);
    if (!mcp.ok) {
      // A refused registration leaves the host unwired, so the companion skill
      // is not installed either.
      return { hostId: host.id, ok: false, messages };
    }

    const skill = await copyPackagedSkill(host.id, env.home);
    messages.push(...skill.messages);
    return { hostId: host.id, ok: mcp.ok && skill.ok, messages };
  },

  async uninstall(env: HostEnvironment): Promise<HostOperationResult> {
    const messages: string[] = [];
    const path = dshPatchPath(env.home);
    if (existsSync(path)) {
      const existing = readFileSync(path, "utf8");
      const others = userPatches(existing);
      // Not an empty FILE: dsh parses the patch as a top-level array and fails
      // boot on anything else, so the last removal leaves [] behind.
      if (others !== existing.trim()) {
        writeFileSync(path, others ? others + "\n" : DSH_EMPTY_LIST + "\n");
      }
      messages.push("dsh: plugin entry removed from " + path);
    }
    removeLegacyProfileRows(env, messages);
    const mcp = await removeMcpServer({
      hostId: host.id,
      home: env.home,
      serverPath: join(env.runtimeDirectory, MCP_SERVER_FILE_NAME),
    });
    messages.push(...mcp.messages);
    const skill = await removeInstalledSkill(host.id, env.home);
    messages.push(...skill.messages);
    return { hostId: host.id, ok: mcp.ok && skill.ok, messages };
  },
};

export const dsh: HostContribution = host;
