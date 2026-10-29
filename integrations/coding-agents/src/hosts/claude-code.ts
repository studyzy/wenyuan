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
 * Claude Code: a settings file that permits comments, a user-scope MCP
 * registration through its own command interface, and a skills directory of
 * its own.
 *
 * Its MCP server lives in the host's own store rather than a config file of
 * ours, so the adapter writes no MCP config: the declared command-line
 * mechanism registers through the host's executable and reports the manual line
 * on fallback.
 */

const host: HostContribution = {
  id: "claude-code",
  displayName: "Claude Code",
  detection: { executable: "claude", configDirectory: ".claude" },
  locations: {
    configFiles: [".claude/settings.json"],
    configFormat: "jsonc",
    mcpConfigFiles: [],
    mcpFormat: "json",
    skillsDirectory: ".claude/skills",
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
    // Read the file back the way Claude Code reads it: a registration it does
    // not parse is a hook that never runs, and install must not claim one.
    const verified = await verifyLifecycleHooks(configPath, hooks.expected, "jsonc");
    const messages: string[] = [...hooks.messages, ...verified.messages];
    if (!verified.ok) {
      messages.push(
        `the hooks are NOT wired: ${configPath} does not let Claude Code run them. ` +
          `Fix the entry by hand, then re-run install.`,
      );
      return { hostId: host.id, ok: false, messages };
    }

    const mcp = await registerMcpServer({
      hostId: host.id,
      home: env.home,
      serverPath: join(env.runtimeDirectory, MCP_SERVER_FILE_NAME),
      environment: options.environment,
      executable: host.detection.executable,
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
      executable: host.detection.executable,
    });
    ok = ok && mcp.ok;
    messages.push(...mcp.messages);
    const skill = await removeInstalledSkill(host.id, env.home);
    messages.push(...skill.messages);
    return { hostId: host.id, ok: ok && skill.ok, messages };
  },
};

export const claudeCode: HostContribution = host;