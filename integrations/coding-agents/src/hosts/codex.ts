import { join } from "node:path";
import { registerMcpServer, removeMcpServer } from "../mcp/register";
import { MCP_SERVER_FILE_NAME } from "../runtime/layout";
import { installLifecycleHooks } from "./hooks";
import { verifyLifecycleHooks } from "./hook-verify";
import { removeLifecycleHooks } from "./hooks-remove";
import { ensureTomlFeature } from "./toml-feature";
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
 * Codex: its hooks live in their own file, its MCP registration is a TOML
 * table, and it reads the shared agentskills-standard skills root.
 *
 * Two things make Codex different from the other hook hosts. Its hook config is
 * `~/.codex/hooks.json` — its `settings.json` is not a file Codex reads — and
 * the hook runner is off unless `~/.codex/config.toml` enables it, so the
 * adapter turns that feature on when the operator has not stated it.
 */

const host: HostContribution = {
  id: "codex",
  displayName: "Codex",
  detection: { executable: "codex", configDirectory: ".codex" },
  locations: {
    configFiles: [".codex/hooks.json"],
    configFormat: "json",
    mcpConfigFiles: [".codex/config.toml"],
    mcpFormat: "json",
    // The agentskills-standard shared root this host reads; the skill still
    // lands in a directory of this host's own inside it.
    skillsDirectory: ".agents/skills",
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
        `the hooks are NOT wired: ${configPath} does not let Codex run them. ` +
          `Fix the entry by hand, then re-run install.`,
      );
      return { hostId: host.id, ok: false, messages };
    }
    messages.push(...(await ensureTomlFeature(join(env.home, ".codex", "config.toml"), "hooks")));

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

export const codex: HostContribution = host;