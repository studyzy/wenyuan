import { join } from "node:path";
import { registerMcpServer, removeMcpServer } from "../mcp/register";
import { MCP_SERVER_FILE_NAME } from "../runtime/layout";
import { copyPackagedSkill, removeInstalledSkill } from "./skills";
import { DSH_PLUGIN_PACKAGE, dshProfiles, installDshPlugin, type DshProfile } from "./dsh-plugin";
import type {
  HostContribution,
  HostEnvironment,
  HostInstallOptions,
  HostOperationResult,
} from "./types";

/**
 * DSH: a host whose memory wiring is a Cordis plugin, so this installer adds it.
 *
 * DSH composes its behaviour from plugins, so WenYuan reaches a DSH session
 * through `integrations/dsh-plugin` (`@studyzy/dsh-wenyuan-memory`): that
 * plugin attaches recall on `agent/created` and `agent/pre-step`, writes the
 * session back on `agent/turn-stopping`, and registers its own tools — all
 * in-process, on the harness's own extension points. Installation adds the
 * published package to every profile DSH already runs (or to the `web` profile
 * when the machine has none) through DSH's own `dsh plugin` forwarder, so DSH's
 * activation rule stays the only one applied.
 *
 * This package therefore writes NO hook config for DSH. Registering
 * Claude-Code-style command hooks beside the plugin would recall twice per
 * prompt and retain the transcript twice, which is worse than either alone.
 * What remains here is the part the plugin does not own: the MCP server entry
 * (for a deployment that wants the server's own tool surface exposed to DSH)
 * and the companion skill.
 */

/** The MCP server map DSH reads. */
export const DSH_MCP_CONFIG = ".dsh/mcp.json";

/** The profile `dsh web` boots, installed into when no profile exists yet. */
export const DSH_DEFAULT_PROFILE = "web";

/** `$DSH_HOME/profiles`, as a path relative to the home directory. */
export const DSH_PROFILES_DIRECTORY = ".dsh/profiles";

async function addMemoryPlugin(env: HostEnvironment): Promise<{ ok: boolean; messages: string[] }> {
  const profiles: DshProfile[] = await dshProfiles(join(env.home, DSH_PROFILES_DIRECTORY), DSH_DEFAULT_PROFILE);
  const messages: string[] = [];
  const refused: string[] = [];
  let dshMissing = false;
  for (const profile of profiles) {
    const result = await installDshPlugin(profile.name);
    messages.push(...result.messages);
    if (!result.ok) {
      refused.push(profile.name);
      dshMissing = dshMissing || result.missingExecutable === true;
    }
  }
  if (refused.length > 0) {
    const remedy: string = dshMissing
      ? `put dsh on PATH and run \`dsh plugin --profile ${refused[0]} add ${DSH_PLUGIN_PACKAGE}\``
      : `run \`dsh plugin --profile ${refused[0]} add ${DSH_PLUGIN_PACKAGE}\` once its cause is fixed`;
    messages.push(`the memory plugin is NOT installed for ${refused.join(", ")}; ${remedy}, then restart DSH`);
    return { ok: false, messages };
  }
  messages.push("restart DSH to load the memory plugin: a running profile keeps the bundles it started with");
  return { ok: true, messages };
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

  async install(env: HostEnvironment, options: HostInstallOptions): Promise<HostOperationResult> {
    // The plugin IS DSH's memory wiring, so a profile that did not receive it
    // leaves the host unwired; the MCP entry and the skill are still written so
    // a retry via `dsh plugin add` needs no second install run.
    const plugin = await addMemoryPlugin(env);

    const mcp = await registerMcpServer({
      hostId: host.id,
      home: env.home,
      serverPath: join(env.runtimeDirectory, MCP_SERVER_FILE_NAME),
      environment: options.environment,
    });
    if (!mcp.ok) {
      // A refused registration leaves the host unwired, so the companion skill
      // is not installed either.
      return { hostId: host.id, ok: false, messages: [...plugin.messages, ...mcp.messages] };
    }

    const skill = await copyPackagedSkill(host.id, env.home);
    const messages: string[] = [...plugin.messages, ...mcp.messages, ...skill.messages];
    return { hostId: host.id, ok: plugin.ok && mcp.ok && skill.ok, messages };
  },

  async uninstall(env: HostEnvironment): Promise<HostOperationResult> {
    // The plugin's bundle entry belongs to DSH profile management: removing it
    // is `dsh plugin --profile <name> remove @studyzy/dsh-wenyuan-memory`.
    const messages: string[] = [
      `${DSH_PLUGIN_PACKAGE} stays installed in each DSH profile; remove it with `
        + `\`dsh plugin --profile <name> remove ${DSH_PLUGIN_PACKAGE}\``,
    ];
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