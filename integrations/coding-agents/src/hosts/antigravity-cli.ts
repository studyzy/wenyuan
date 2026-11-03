import { join } from "node:path";
import { registerMcpServer, removeMcpServer } from "../mcp/register";
import { MCP_SERVER_FILE_NAME } from "../runtime/layout";
import { hookContainerPath } from "./lifecycle";
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
 * Antigravity: the Gemini CLI's config directory, with hooks and MCP beside
 * the Gemini settings.
 *
 * Two things make its hook config unlike the others. The events live in an
 * integration namespace key of their own - this package's runtime directory
 * name - so the bundle cannot collide with other global hook bundles the
 * file may name; and its registration shape is one plain command object per
 * event, with no execution type and no matcher group, so the lifecycle
 * declares the plain-entry style. It has no SessionStart event: both
 * interactive stages declare PreInvocation, and the shared declaration's
 * dedup registers the prompt stage's entry once.
 *
 * Detection is deliberately the executable alone: the ~/.gemini directory
 * is shared with the legacy Gemini CLI, whose files prove nothing about
 * Antigravity being usable.
 */

/** The hooks file Antigravity reads, with one namespace per integration. */
export const ANTIGRAVITY_HOOKS = ".gemini/config/hooks.json";

const host: HostContribution = {
  id: "antigravity-cli",
  displayName: "Antigravity",
  detection: { executable: "agy", configDirectory: "" },
  locations: {
    configFiles: [ANTIGRAVITY_HOOKS],
    configFormat: "json",
    mcpConfigFiles: [".gemini/config/mcp_config.json"],
    mcpFormat: "json",
    skillsDirectory: ".gemini/config/skills",
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
    // The events live in this package's own namespace key at the root.
    const verified = await verifyLifecycleHooks(configPath, hooks.expected, "json", [
      ...hookContainerPath(host.id),
    ]);
    const messages: string[] = [...hooks.messages, ...verified.messages];
    if (!verified.ok) {
      messages.push(
        "the hooks are NOT wired: " + configPath + " does not let Antigravity run them. " +
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

export const antigravityCli: HostContribution = host;
