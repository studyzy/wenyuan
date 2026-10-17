import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import {
  inspectMcpServer,
  registerMcpServer,
  removeMcpServer,
} from "../mcp/register";
import { MCP_SERVER_FILE_NAME } from "../runtime/layout";
import { backupExistingFile } from "../write/backup";
import { upsertJsoncMember } from "../write/jsonc-edit";
import { installLifecycleHooks } from "./hooks";
import { removeLifecycleHooks } from "./hooks-remove";
import { copyPackagedSkill, removeInstalledSkill } from "./skills";
import {
  hostConfigPath,
  hostConfigPaths,
  hostMcpConfigPath,
  hostMcpConfigPaths,
  hostSkillsPath,
  type HostContribution,
  type HostEnvironment,
  type HostInstallOptions,
  type HostOperationResult,
} from "./types";

/**
 * Claude Code: a settings file that permits comments, a user-scope MCP config
 * beside the home directory, and a skills directory of its own.
 *
 * The host's global workspace-MCP setting is machine-wide, so it is only
 * flipped when the operator authorizes it; without that, the manual step is
 * reported and the setting is left exactly as it is.
 */

const host: HostContribution = {
  id: "claude-code",
  displayName: "Claude Code",
  detection: { executable: "claude", configDirectory: ".claude" },
  locations: {
    configFiles: [".claude/settings.json"],
    configFormat: "jsonc",
    mcpConfigFiles: [".claude.json"],
    mcpFormat: "json",
    skillsDirectory: ".claude/skills",
  },
  workspaceMcpKey: "enableAllProjectMcpServers",

  async install(env: HostEnvironment, options: HostInstallOptions): Promise<HostOperationResult> {
    const configPath: string = hostConfigPath(host, env.home);
    const mcpPath: string = hostMcpConfigPath(host, env.home);
    const ownership = await inspectMcpServer({
      configPath: mcpPath,
      format: host.locations.mcpFormat,
      hostId: host.id,
    });
    if (ownership === "foreign") {
      return {
        hostId: host.id,
        ok: false,
        messages: [
          `a different MCP server is already registered under this package's name in ${mcpPath}, so ${host.displayName} was not wired.`,
        ],
      };
    }

    const hooks = await installLifecycleHooks({
      hostId: host.id,
      configPath,
      format: host.locations.configFormat,
      runtimeDirectory: env.runtimeDirectory,
    });
    if (!hooks.ok) {
      return { hostId: host.id, ok: false, messages: hooks.messages };
    }

    const messages: string[] = [...hooks.messages];
    messages.push(...(await applyWorkspaceMcpSetting(configPath, options)));

    const mcp = await registerMcpServer({
      configPath: mcpPath,
      format: host.locations.mcpFormat,
      hostId: host.id,
      serverPath: join(env.runtimeDirectory, MCP_SERVER_FILE_NAME),
      environment: options.environment,
    });
    messages.push(...mcp.messages);

    const skill = await copyPackagedSkill(hostSkillsPath(host, env.home), host.id);
    messages.push(...skill.messages);

    return { hostId: host.id, ok: mcp.ok && skill.ok, messages };
  },

  async uninstall(env: HostEnvironment): Promise<HostOperationResult> {
    const messages: string[] = [];
    let ok = true;
    for (const configPath of hostConfigPaths(host, env.home)) {
      const result = await removeLifecycleHooks({
        hostId: host.id,
        configPath,
        format: host.locations.configFormat,
      });
      ok = ok && result.ok;
      messages.push(...result.messages);
    }
    for (const mcpPath of hostMcpConfigPaths(host, env.home)) {
      const result = await removeMcpServer({
        configPath: mcpPath,
        format: host.locations.mcpFormat,
        hostId: host.id,
      });
      ok = ok && result.ok;
      messages.push(...result.messages);
    }
    const skill = await removeInstalledSkill(hostSkillsPath(host, env.home), host.id);
    messages.push(...skill.messages);
    return { hostId: host.id, ok: ok && skill.ok, messages };
  },
};

/**
 * The workspace-MCP setting is global to the machine, so a non-interactive run
 * without the opt-in flag refuses to flip it and prints what the operator can
 * do instead.
 */
async function applyWorkspaceMcpSetting(
  configPath: string,
  options: HostInstallOptions,
): Promise<string[]> {
  const key: string | undefined = host.workspaceMcpKey;
  if (!key) {
    return [];
  }
  if (!options.enableWorkspaceMcp) {
    return [
      `left "${key}" untouched: pass the workspace-MCP opt-in flag to change it, or set "${key}": true in ${configPath} yourself.`,
    ];
  }
  const source: string = await readFile(configPath, "utf8").catch(() => "");
  const edit = upsertJsoncMember(source, [], key, true, () => true);
  if (edit && edit.changed) {
    await backupExistingFile(configPath);
    await writeFile(configPath, edit.text, { mode: 0o600 });
  }
  return [];
}

export const claudeCode: HostContribution = host;
