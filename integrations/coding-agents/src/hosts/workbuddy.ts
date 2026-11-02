import { join } from "node:path";
import { registerMcpServer, removeMcpServer } from "../mcp/register";
import { WORKBUDDY_MCP_PATH } from "../mcp/mechanisms";
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
 * WorkBuddy: a settings file that permits comments, one fixed JSON server-map
 * MCP file and a skills directory of its own.
 *
 * The registration shape is the one CodeBuddy reads — a matcher-group per event
 * under `hooks`, the PascalCase event names and a seconds budget — so the
 * adapter differs from CodeBuddy's in its locations, not in its mechanism. Two
 * of those locations are deliberately narrower than CodeBuddy's:
 *
 * - the hook config is one file (`~/.workbuddy/settings.json`), not a candidate
 *   chain, and it is edited as text so a comment or an unrelated member in a
 *   hand-maintained settings file survives an install;
 * - the MCP registration is one fixed file (`~/.workbuddy/mcp.json`). WorkBuddy
 *   reads that path and no fallback, so a candidate list here would only let an
 *   install write a file the host never opens.
 */

/** The settings file WorkBuddy reads its hooks from. */
export const WORKBUDDY_CONFIG = ".workbuddy/settings.json";

/** The skills root WorkBuddy loads a companion skill from. */
export const WORKBUDDY_SKILLS_DIRECTORY = ".workbuddy/skills";

const host: HostContribution = {
  id: "workbuddy",
  displayName: "WorkBuddy",
  detection: { executable: "workbuddy", configDirectory: ".workbuddy" },
  locations: {
    configFiles: [WORKBUDDY_CONFIG],
    // Edited as text: the file is hand-maintained, so comments and formatting
    // are preserved rather than rewritten by a whole-document merge.
    configFormat: "jsonc",
    mcpConfigFiles: [WORKBUDDY_MCP_PATH],
    mcpFormat: "json",
    skillsDirectory: WORKBUDDY_SKILLS_DIRECTORY,
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
    const verified = await verifyLifecycleHooks(configPath, hooks.expected, host.locations.configFormat);
    const messages: string[] = [...hooks.messages, ...verified.messages];
    if (!verified.ok) {
      messages.push(
        `the hooks are NOT wired: ${configPath} does not let WorkBuddy run them. ` +
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

export const workbuddy: HostContribution = host;
