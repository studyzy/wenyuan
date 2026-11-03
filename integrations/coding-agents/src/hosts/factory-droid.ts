import { readFile, mkdir, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { inspectMcpServer, registerMcpServer, removeMcpServer } from "../mcp/register";
import { MCP_SERVER_FILE_NAME } from "../runtime/layout";
import { backupExistingFile } from "../write/backup";
import { asJsonDocument } from "../write/json-merge";
import { HOOKS_CONTAINER_KEY } from "./hook-shape";
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
 * Factory Droid: plain JSON files under ~/.factory, so the adapter needs no
 * Droid CLI round-trip.
 *
 * Its user-level hooks.json is the EVENT MAP itself - no wrapping hooks
 * member, unlike the matcher-group hosts that nest under one - while the
 * matcher-group entries are the shape Claude Code reads. A standalone
 * hooks.json takes precedence over settings.json, so when this package
 * creates one it first copies any hook declarations the host would otherwise
 * have read from settings.json: creating the file empty would silently
 * disable every hook the user already had.
 */

/** The user-level hook registrations Factory Droid reads. */
export const DROID_HOOKS = ".factory/hooks.json";

/** The settings file whose hooks member is the fallback when hooks.json is absent. */
export const DROID_SETTINGS = ".factory/settings.json";

/**
 * Copies the fallback declarations before the standalone file is created, so
 * installing does not disable the hooks the host already ran from settings.json.
 */
export async function seedFromSettings(configPath: string, home: string): Promise<boolean> {
  let existing: string | null = null;
  try {
    existing = await readFile(configPath, "utf8");
  } catch {
    existing = null;
  }
  if (existing !== null) {
    return false;
  }
  let settingsText: string;
  try {
    settingsText = await readFile(join(home, DROID_SETTINGS), "utf8");
  } catch {
    return false;
  }
  const fallback: unknown = asJsonDocument(settingsText)[HOOKS_CONTAINER_KEY];
  if (fallback === null || typeof fallback !== "object" || Array.isArray(fallback)) {
    return false;
  }
  await backupExistingFile(join(home, DROID_SETTINGS));
  await mkdir(dirname(configPath), { recursive: true });
  await writeFile(configPath, JSON.stringify(fallback, null, 2) + "\n", { mode: 0o600 });
  return true;
}

const host: HostContribution = {
  id: "factory-droid",
  displayName: "Factory Droid",
  detection: { executable: "droid", configDirectory: ".factory" },
  locations: {
    configFiles: [DROID_HOOKS],
    configFormat: "json",
    mcpConfigFiles: [".factory/mcp.json"],
    mcpFormat: "json",
    skillsDirectory: ".factory/skills",
  },

  async preflight(env: HostEnvironment): Promise<string | null> {
    const ownership = await inspectMcpServer({
      hostId: host.id,
      home: env.home,
      serverPath: join(env.runtimeDirectory, MCP_SERVER_FILE_NAME),
    });
    if (ownership === "foreign") {
      return (
        join(env.home, ".factory/mcp.json") +
        ' already contains a user-managed MCP server named "wenyuan-memory". ' +
        "Rename or remove that entry, then re-run install."
      );
    }
    return null;
  },

  async install(env: HostEnvironment, options: HostInstallOptions): Promise<HostOperationResult> {
    const configPath: string = hostConfigPath(host, env.home) ?? "";
    await seedFromSettings(configPath, env.home);

    const hooks = await installLifecycleHooks({
      hostId: host.id,
      configPath,
      format: host.locations.configFormat,
      runtimeDirectory: env.runtimeDirectory,
    });
    if (!hooks.ok) {
      return { hostId: host.id, ok: false, messages: hooks.messages };
    }
    // The event map is the file itself, so the read-back walks no container.
    const verified = await verifyLifecycleHooks(configPath, hooks.expected, "json", []);
    const messages: string[] = [...hooks.messages, ...verified.messages];
    if (!verified.ok) {
      messages.push(
        "the hooks are NOT wired: " + configPath + " does not let Factory Droid run them. " +
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

export const factoryDroid: HostContribution = host;
