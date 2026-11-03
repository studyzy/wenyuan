import { join } from "node:path";
import { registerMcpServer, removeMcpServer } from "../mcp/register";
import { MCP_SERVER_FILE_NAME } from "../runtime/layout";
import { installLifecycleHooks } from "./hooks";
import { verifyLifecycleHooks } from "./hook-verify";
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
 * Grok Build: a native hooks file of its own and MCP server tables in the
 * same config.toml its CLI reads.
 *
 * Grok hands the model additionalContext from TOOL hooks only: SessionStart
 * is observe-only and UserPromptSubmit can only block, so beside the three
 * lifecycle events the adapter registers the pre-prompt script on
 * PostToolUse as well - that registration is what actually delivers the
 * recalled context beside the next tool result. The event spellings are the
 * reference host's matcher groups; the payload envelope is camelCase where
 * Claude's is snake_case, which is a runtime reading concern, not a
 * registration one.
 */

/** The hooks file Grok Build reads its lifecycle registrations from. */
export const GROK_HOOKS = ".grok/hooks/hooks.json";

/** The config file Grok Build reads its MCP server tables from. */
export const GROK_CONFIG = ".grok/config.toml";

const host: HostContribution = {
  id: "grok-build",
  displayName: "Grok Build",
  detection: { executable: "grok", configDirectory: ".grok" },
  locations: {
    configFiles: [GROK_HOOKS],
    configFormat: "json",
    mcpConfigFiles: [GROK_CONFIG],
    mcpFormat: "json",
    skillsDirectory: ".grok/skills",
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
    const verified = await verifyLifecycleHooks(configPath, hooks.expected, "json");
    const messages: string[] = [...hooks.messages, ...verified.messages];
    if (!verified.ok) {
      messages.push(
        "the hooks are NOT wired: " + configPath + " does not let Grok Build run them. " +
          "Fix the entry by hand, then re-run install.",
      );
      return { hostId: host.id, ok: false, messages };
    }

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

export const grokBuild: HostContribution = host;
