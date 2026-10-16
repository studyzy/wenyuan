import { join } from "node:path";
import { inspectMcpServer, registerMcpServer, removeMcpServer } from "../mcp/register";
import { MCP_SERVER_FILE_NAME } from "../runtime/layout";
import { installLifecycleHooks, removeLifecycleHooks } from "./hooks";
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
 * CodeBuddy: a settings file that permits comments, an MCP config location of
 * its own, and a skills directory of its own.
 *
 * Each host installs into a directory named for itself under its skills root,
 * so a root shared with another host still leaves both copies independent.
 */

const host: HostContribution = {
  id: "codebuddy",
  displayName: "CodeBuddy",
  detection: { executable: "codebuddy", configDirectory: ".codebuddy" },
  locations: {
    configFiles: [".codebuddy/settings.json"],
    configFormat: "jsonc",
    mcpConfigFiles: [".codebuddy/mcp.json"],
    mcpFormat: "json",
    skillsDirectory: ".codebuddy/skills",
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
      configPath,
      format: host.locations.configFormat,
      runtimeDirectory: env.runtimeDirectory,
      environment: options.environment,
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

export const codebuddy: HostContribution = host;
