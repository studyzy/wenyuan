import { join } from "node:path";
import { registerMcpServer, removeMcpServer } from "../mcp/register";
import { MCP_SERVER_FILE_NAME } from "../runtime/layout";
import { installLifecycleHooks } from "./hooks";
import { removeLifecycleHooks } from "./hooks-remove";
import { copyPackagedSkill, removeInstalledSkill } from "./skills";
import {
  hostConfigPath,
  hostConfigPaths,
  type HostContribution,
  type HostEnvironment,
  type HostInstallOptions,
  type HostOperationResult,
} from "./types";

/**
 * CodeBuddy: a settings file that permits comments, a JSON server-map MCP
 * registration resolved through a priority-ordered candidate list, and a skills
 * directory of its own.
 *
 * The adapter writes no entry inline: the declared JSON-server-map mechanism
 * resolves the candidate, merges one member of ours, and sweeps every candidate
 * on uninstall. Each host installs into a directory named for itself under its
 * skills root, so a root shared with another host still leaves both copies
 * independent.
 */

const host: HostContribution = {
  id: "codebuddy",
  displayName: "CodeBuddy",
  detection: { executable: "codebuddy", configDirectory: ".codebuddy" },
  locations: {
    configFiles: [".codebuddy/settings.json"],
    configFormat: "jsonc",
    mcpConfigFiles: [".codebuddy/.mcp.json", ".codebuddy/mcp.json", ".codebuddy.json"],
    mcpFormat: "json",
    skillsDirectory: ".codebuddy/skills",
  },

  async install(env: HostEnvironment, options: HostInstallOptions): Promise<HostOperationResult> {
    const configPath: string = hostConfigPath(host, env.home) ?? "";

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

    const skill = await copyPackagedSkill(host.id, env.home);
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
    const skill = await removeInstalledSkill(host.id, env.home);
    messages.push(...skill.messages);
    return { hostId: host.id, ok: ok && skill.ok, messages };
  },
};

export const codebuddy: HostContribution = host;
