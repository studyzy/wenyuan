import { join } from "node:path";
import { registerMcpServer, removeMcpServer } from "../mcp/register";
import { MCP_SERVER_FILE_NAME } from "../runtime/layout";
import {
  installKimiBlock,
  kimiBlockEvents,
  kimiScriptPath,
  removeKimiBlock,
  renderKimiBlock,
} from "./kimi-block";
import { copyPackagedSkill, removeInstalledSkill } from "./skills";
import {
  type HostContribution,
  type HostEnvironment,
  type HostInstallOptions,
  type HostOperationResult,
} from "./types";

/**
 * Kimi Code: hooks as a marked TOML block inside its config.toml, an MCP
 * server map of its own beside it, and a skills root under the same home.
 *
 * Every path resolves under the home-relative .kimi-code directory, the same
 * resolution the shared skill locations and the session-start refresh use, so
 * install, refresh and removal can never disagree about where the host's
 * state lives. A deployment that relocates the CLI's home through its own
 * environment variable would need the redirect named in every one of those
 * layers at once; until it does, they all name the default.
 */

/** The config.toml Kimi Code reads its hooks block from. */
export const KIMI_CONFIG = ".kimi-code/config.toml";

/** The server-map file beside it. */
export const KIMI_MCP = ".kimi-code/mcp.json";

const host: HostContribution = {
  id: "kimi",
  displayName: "Kimi Code",
  detection: { executable: "kimi", configDirectory: ".kimi-code" },
  locations: {
    configFiles: [".kimi-code/config.toml"],
    configFormat: "json",
    mcpConfigFiles: [".kimi-code/mcp.json"],
    mcpFormat: "json",
    skillsDirectory: ".kimi-code/skills",
  },

  async install(env: HostEnvironment, options: HostInstallOptions): Promise<HostOperationResult> {
    const configPath: string = join(env.home, KIMI_CONFIG);
    const messages: string[] = [];

    const block: string = renderKimiBlock(host.id, (scriptFile) =>
      kimiScriptPath(env.runtimeDirectory, scriptFile),
    );
    const written: boolean = await installKimiBlock(configPath, block);
    messages.push(
      written
        ? `the lifecycle hooks were written into ${configPath}`
        : `the lifecycle hooks in ${configPath} were already current`,
    );

    // The read-back check, read the way the block was written: an event whose
    // table does not name the staged script is a hook that never runs.
    const events: Map<string, string> = kimiBlockEvents(block);
    const missing: string[] = [];
    for (const [event, command] of events) {
      if (!command.includes(env.runtimeDirectory)) {
        missing.push(event);
      }
    }
    if (missing.length > 0) {
      messages.push(
        `the hooks are NOT wired: ${configPath} does not let Kimi Code run ${missing.join(", ")}. ` +
          `Fix the entries by hand, then re-run install.`,
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
    const configPath: string = join(env.home, KIMI_CONFIG);
    const blockRemoved: boolean = await removeKimiBlock(configPath);
    messages.push(
      blockRemoved
        ? `the lifecycle hooks were removed from ${configPath}`
        : `no lifecycle hooks of ours were found in ${configPath}`,
    );
    const mcp = await removeMcpServer({
      hostId: host.id,
      home: env.home,
      serverPath: join(env.runtimeDirectory, MCP_SERVER_FILE_NAME),
    });
    messages.push(...mcp.messages);
    const skill = await removeInstalledSkill(host.id, env.home);
    messages.push(...skill.messages);
    return { hostId: host.id, ok: mcp.ok && skill.ok, messages };
  },
};

export const kimi: HostContribution = host;
