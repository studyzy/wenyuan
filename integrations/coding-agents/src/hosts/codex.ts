import { join } from "node:path";
import { inspectMcpServer, registerMcpServer, removeMcpServer } from "../mcp/register";
import { MCP_SERVER_FILE_NAME } from "../runtime/layout";
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
 * Codex: its own config location for the lifecycle hooks, a separate MCP
 * config location, and its own skills directory.
 */

const host: HostContribution = {
  id: "codex",
  displayName: "Codex",
  detection: { executable: "codex", configDirectory: ".codex" },
  locations: {
    configFiles: [".codex/settings.json"],
    configFormat: "json",
    mcpConfigFiles: [".codex/mcp.json", ".codex/settings.json"],
    mcpFormat: "json",
    skillsDirectory: ".codex/skills",
  },

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

    const mcp = await registerMcpServer({
      configPath: mcpPath,
      format: host.locations.mcpFormat,
      hostId: host.id,
      serverPath: join(env.runtimeDirectory, MCP_SERVER_FILE_NAME),
      environment: options.environment,
    });

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

export const codex: HostContribution = host;
