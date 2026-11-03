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
 * Qwen Code: Claude Code's hook registration shape and protocol, with two
 * differences the adapter carries. The budget the host reads is in
 * milliseconds (it passes the value straight to a timer, so a seconds
 * spelling would kill the hooks before a Node process starts), and its MCP
 * registration goes through the host's own command interface spelled the
 * Gemini CLI's way.
 *
 * The settings file is edited as text: like Claude Code's settings it may
 * carry comments, and a whole-document rewrite would strip them.
 */

/** The settings file Qwen Code reads its hooks from. */
export const QWEN_SETTINGS = ".qwen/settings.json";

const host: HostContribution = {
  id: "qwen-code",
  displayName: "Qwen Code",
  detection: { executable: "qwen", configDirectory: ".qwen" },
  locations: {
    configFiles: [QWEN_SETTINGS],
    configFormat: "jsonc",
    mcpConfigFiles: [],
    mcpFormat: "json",
    skillsDirectory: ".qwen/skills",
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
    const verified = await verifyLifecycleHooks(configPath, hooks.expected, "jsonc");
    const messages: string[] = [...hooks.messages, ...verified.messages];
    if (!verified.ok) {
      messages.push(
        `the hooks are NOT wired: ${configPath} does not let Qwen Code run them. ` +
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

export const qwenCode: HostContribution = host;
