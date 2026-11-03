import { join } from "node:path";
import { inspectMcpServer, registerMcpServer, removeMcpServer } from "../mcp/register";
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
 * ZCode: one CLI config that carries the hook registrations, the MCP server
 * map and the model routing, and deliberately NOT the Claude Code settings
 * file the embedded runtime would also read - writing that would wire a
 * second agent nobody asked for.
 *
 * Three things make the block different from the other JSON hosts: the hook
 * runner ships disabled, so install turns it on or the lifecycle never
 * fires; the output cap drops oversized hook output whole, taking the
 * injection with it, so install fills the cap only when the user has not
 * tuned it; and the registrations live under hooks.events in the process
 * argv shape, because ZCode spawns hooks without a shell.
 */

/** The CLI config ZCode reads its hooks and MCP servers from. */
export const ZCODE_CONFIG = ".zcode/cli/config.json";

const host: HostContribution = {
  id: "zcode",
  displayName: "ZCode",
  detection: { executable: "zcode", configDirectory: ".zcode" },
  locations: {
    configFiles: [ZCODE_CONFIG],
    configFormat: "json",
    // The server map lives inside the same config, so the MCP mechanism
    // writes there and no second file of ours exists.
    mcpConfigFiles: [ZCODE_CONFIG],
    mcpFormat: "json",
    // ZCode's own user-level skill root, not the shared agentskills root it
    // also scans: removal is by fixed directory name, and the shared root
    // would let one host's uninstall take another host's copy with it.
    skillsDirectory: ".zcode/skills",
  },

  async preflight(env: HostEnvironment): Promise<string | null> {
    const ownership = await inspectMcpServer({
      hostId: host.id,
      home: env.home,
      serverPath: join(env.runtimeDirectory, MCP_SERVER_FILE_NAME),
    });
    if (ownership === "foreign") {
      return (
        join(env.home, ZCODE_CONFIG) +
        ' already contains a user-managed MCP server named "wenyuan-memory". ' +
        "Rename or remove that entry, then re-run install."
      );
    }
    return null;
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
    // The events live under hooks.events, one level below the wrapper.
    const verified = await verifyLifecycleHooks(configPath, hooks.expected, "json", [
      ...hookContainerPath(host.id),
    ]);
    const messages: string[] = [...hooks.messages, ...verified.messages];
    if (!verified.ok) {
      messages.push(
        "the hooks are NOT wired: " + configPath + " does not let ZCode run them. " +
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

export const zcode: HostContribution = host;
