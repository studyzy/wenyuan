import { join } from "node:path";
import { registerMcpServer, removeMcpServer } from "../mcp/register";
import { MCP_SERVER_FILE_NAME } from "../runtime/layout";
import { installLifecycleHooks } from "./hooks";
import { removeLifecycleHooks } from "./hooks-remove";
import { copyPackagedSkill, removeInstalledSkill } from "./skills";
import {
  hostConfigPath,
  hostConfigPaths,
  hostSkillsPath,
  type HostContribution,
  type HostEnvironment,
  type HostInstallOptions,
  type HostOperationResult,
} from "./types";

/**
 * Codex: its own config location for the lifecycle hooks, a TOML config file
 * for the MCP registration, and its own skills directory.
 *
 * The MCP entry is a TOML table named after this package's server under the
 * host's server-container key, so the adapter writes no entry inline: the
 * declared TOML-table mechanism reads, strips and rewrites the block.
 */

const host: HostContribution = {
  id: "codex",
  displayName: "Codex",
  detection: { executable: "codex", configDirectory: ".codex" },
  locations: {
    configFiles: [".codex/settings.json"],
    configFormat: "json",
    mcpConfigFiles: [".codex/config.toml"],
    mcpFormat: "json",
    skillsDirectory: ".codex/skills",
  },

  async install(env: HostEnvironment, options: HostInstallOptions): Promise<HostOperationResult> {
    const configPath: string = hostConfigPath(host, env.home);

    const hooks = await installLifecycleHooks({
      hostId: host.id,
      configPath,
      format: host.locations.configFormat,
      runtimeDirectory: env.runtimeDirectory,
    });
    if (!hooks.ok) {
      return { hostId: host.id, ok: false, messages: hooks.messages };
    }

    const mcp = await registerMcpServer({
      hostId: host.id,
      home: env.home,
      serverPath: join(env.runtimeDirectory, MCP_SERVER_FILE_NAME),
      environment: options.environment,
    });
    if (!mcp.ok) {
      // A refused registration leaves the host unwired, so the companion skill
      // is not installed either.
      return { hostId: host.id, ok: false, messages: [...hooks.messages, ...mcp.messages] };
    }

    const skill = await copyPackagedSkill(hostSkillsPath(host, env.home), host.id);
    const messages: string[] = [...hooks.messages, ...mcp.messages, ...skill.messages];
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
    const mcp = await removeMcpServer({
      hostId: host.id,
      home: env.home,
      serverPath: join(env.runtimeDirectory, MCP_SERVER_FILE_NAME),
    });
    ok = ok && mcp.ok;
    messages.push(...mcp.messages);
    const skill = await removeInstalledSkill(hostSkillsPath(host, env.home), host.id);
    messages.push(...skill.messages);
    return { hostId: host.id, ok: ok && skill.ok, messages };
  },
};

export const codex: HostContribution = host;
