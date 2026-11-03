import { spawn } from "node:child_process";
import { join } from "node:path";
import { registerMcpServer, removeMcpServer } from "../mcp/register";
import { MCP_SERVER_FILE_NAME } from "../runtime/layout";
import { installLifecycleHooks } from "./hooks";
import { verifyLifecycleHooks } from "./hook-verify";
import { removeLifecycleHooks } from "./hooks-remove";
import {
  hostConfigPath,
  hostConfigPaths,
  type HostContribution,
  type HostEnvironment,
  type HostInstallOptions,
  type HostOperationResult,
} from "./types";

/**
 * Devin: a settings config that carries its hooks, a JSON server map beside it,
 * and no skills mechanism.
 *
 * Devin is the one host whose hooks never hand over a transcript: they carry a
 * session id and nothing else, so the write-back can only work by reading the
 * CLI's own session store. That store is a SQLite database, which makes SQLite
 * support in the node that launches the hooks a hard prerequisite — and one
 * worth probing at install time, because the alternative is an install that
 * looks healthy and remembers nothing.
 */

/** The hooks config Devin reads. */
export const DEVIN_CONFIG = ".config/devin/config.json";

/**
 * Probes the node on PATH — not this process — because that is the interpreter
 * the installed hook command runs under. An installer started through a
 * version manager or a wrapper is easily a different build.
 */
export function nodeOnPathHasSqlite(node = "node"): Promise<boolean> {
  return new Promise((resolve) => {
    let child;
    try {
      child = spawn(node, ["-e", "require('node:sqlite')"], {
        stdio: "ignore",
        shell: false,
      });
    } catch {
      resolve(false);
      return;
    }
    child.on("error", () => resolve(false));
    child.on("close", (code: number | null) => resolve(code === 0));
  });
}

const host: HostContribution = {
  id: "devin-cli",
  displayName: "Devin",
  detection: { executable: "devin", configDirectory: ".config/devin" },
  locations: {
    configFiles: [DEVIN_CONFIG],
    configFormat: "json",
    mcpConfigFiles: [".config/devin/mcp_config.json"],
    mcpFormat: "json",
    skillsDirectory: "",
  },

  async preflight(_env: HostEnvironment): Promise<string | null> {
    if (await nodeOnPathHasSqlite()) {
      return null;
    }
    return (
      "node:sqlite is unavailable in the node on PATH. Devin keeps its " +
      "conversations in its own session database and its hooks pass only a " +
      "session id, so without SQLite nothing could ever be retained. " +
      "Upgrade to Node 22.5 or newer and re-run install."
    );
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
        `the hooks are NOT wired: ${configPath} does not let Devin run them. ` +
          `Fix the entry by hand, then re-run install.`,
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
    return { hostId: host.id, ok: mcp.ok, messages };
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
    return { hostId: host.id, ok, messages };
  },
};

export const devinCli: HostContribution = host;
