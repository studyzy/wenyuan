import { existsSync } from "node:fs";
import { mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { entryIsOwned } from "../mcp/ownership";
import { PER_USER_DIRECTORY_NAME } from "../runtime/layout";
import { backupExistingFile } from "../write/backup";
import { asJsonDocument, type JsonDocument } from "../write/json-merge";
import { installLifecycleHooks } from "./hooks";
import { hookContainerPath } from "./lifecycle";
import { verifyLifecycleHooks } from "./hook-verify";
import { removeLifecycleHooks } from "./hooks-remove";
import { traecodeDotDirectory } from "./skill-locations";
import { copyPackagedSkill, removeInstalledSkill } from "./skills";
import {
  hostConfigPaths,
  type HostContribution,
  type HostEnvironment,
  type HostInstallOptions,
  type HostOperationResult,
} from "./types";

/**
 * TraeCode: plain JSON files under the installed edition's dot-directory, and
 * the app's Electron user-data directory beside it.
 *
 * The host speaks Claude Code's hook protocol with the registrations nested
 * under the top-level hooks key of its hooks.json (unlike Factory Droid, whose
 * event map IS the file), and it writes and expects a document version, which
 * install seeds at 1 when absent and never overwrites.
 *
 * Deliberately NOT written: any user-level MCP registration. Trae launches
 * user-level servers with the Electron process's working directory, where a
 * per-user opt-in server disables itself and an always-on one resolves the
 * home bank instead of the repository's. The only correct registration is the
 * per-repository .trae/mcp.json file the session-start wiring maintains, so
 * this adapter instead migrates a stale user-level entry of ours away.
 *
 * Trae also runs every hook inside its sandbox, whose generated profile covers
 * neither this package's per-user directory (the hook logs and config) nor the
 * per-window storage the session-start wiring seeds, so install adds both as
 * filesystem read-write rules.
 */

/** The global setting that gates every workspace-level MCP file. */
export const TRAE_WORKSPACE_MCP_KEY = "trae.mcp.enableWorkspaceMcp";

/** An environment value a deployment may leave empty, read as unset. */
function defined(value: string | undefined): string | undefined {
  return value !== undefined && value.length > 0 ? value : undefined;
}

/** The Electron user-data directory of the installed edition, absolute. */
export function traecodeUserDataDirectory(home: string): string {
  const root: string =
    process.platform === "darwin"
      ? join(home, "Library", "Application Support")
      : process.platform === "win32"
        ? (process.env.APPDATA ?? join(home, "AppData", "Roaming"))
        : (defined(process.env.XDG_CONFIG_HOME) ?? join(home, ".config"));
  for (const brand of ["Trae CN", "Trae"]) {
    const directory: string = join(root, brand);
    if (existsSync(directory)) {
      return directory;
    }
  }
  return join(root, "Trae CN");
}

/** The user-level MCP file Trae launches servers from, and never for us. */
export function traecodeUserMcpPath(home: string): string {
  return join(traecodeUserDataDirectory(home), "User", "mcp.json");
}

/** The user settings file carrying the workspace-MCP gate. */
export function traecodeUserSettingsPath(home: string): string {
  return join(traecodeUserDataDirectory(home), "User", "settings.json");
}

/**
 * Removes this package's stale user-level MCP entry, leaving foreign entries
 * and the rest of the document alone, and deleting the file when nothing but
 * our entry was in it. Returns whether anything was removed. Never throws.
 */
export async function removeTraecodeUserMcpEntry(home: string): Promise<boolean> {
  try {
    const mcpPath: string = traecodeUserMcpPath(home);
    let text: string;
    try {
      text = await readFile(mcpPath, "utf8");
    } catch {
      return false;
    }
    const document: JsonDocument = asJsonDocument(text);
    const servers: unknown = (document["mcpServers"] as JsonDocument | undefined)?.["wenyuan-memory"];
    if (!entryIsOwned(servers)) {
      return false;
    }
    const container: JsonDocument = document["mcpServers"] as JsonDocument;
    delete container["wenyuan-memory"];
    const next: JsonDocument = Object.keys(container).length > 0 ? document : {};
    if (Object.keys(next).length === 0) {
      await rm(mcpPath, { force: true });
    } else {
      await writeFile(mcpPath, `${JSON.stringify(document, null, 2)}\n`);
    }
    return true;
  } catch {
    return false;
  }
}

/**
 * Flips the workspace-MCP gate in the user settings, plain JSON only. A file
 * that permits comments is not ours to rewrite: a whole-document round-trip
 * would strip the comments, so the manual step is reported instead.
 */
export async function enableTraecodeWorkspaceMcp(home: string): Promise<boolean> {
  const settingsPath: string = traecodeUserSettingsPath(home);
  let text: string | null = null;
  try {
    text = await readFile(settingsPath, "utf8");
  } catch {
    text = null;
  }
  // A file that will not parse as plain JSON is not ours to rewrite: the edit
  // would strip every comment a hand-maintained settings file carries.
  if (text !== null) {
    try {
      JSON.parse(text);
    } catch {
      return false;
    }
  }
  const document: JsonDocument = text === null ? {} : asJsonDocument(text);
  document[TRAE_WORKSPACE_MCP_KEY] = true;
  await mkdir(dirname(settingsPath), { recursive: true });
  await writeFile(settingsPath, `${JSON.stringify(document, null, 2)}\n`, { mode: 0o600 });
  return true;
}

/** Adds the sandbox read-write rules the hooks need, once each. */
async function addSandboxRules(home: string, dotDirectory: string): Promise<string[]> {
  const sandboxPath: string = join(home, dotDirectory, "sandbox.json");
  const document: JsonDocument = asJsonDocument(await readOrNull(sandboxPath) ?? "");
  const filesystem: JsonDocument = isObject(document["filesystem"])
    ? (document["filesystem"] as JsonDocument)
    : {};
  const readWrite: unknown[] = Array.isArray(filesystem["readWrite"]) ? (filesystem["readWrite"] as unknown[]) : [];
  const needed: string[] = [
    join(home, PER_USER_DIRECTORY_NAME),
    join(traecodeUserDataDirectory(home), "User", "workspaceStorage"),
  ];
  const added: string[] = [];
  for (const directory of needed) {
    if (!readWrite.includes(directory)) {
      readWrite.push(directory);
      added.push(directory);
    }
  }
  if (added.length > 0) {
    filesystem["readWrite"] = readWrite;
    document["filesystem"] = filesystem;
    await backupExistingFile(sandboxPath);
    await mkdir(dirname(sandboxPath), { recursive: true });
    await writeFile(sandboxPath, `${JSON.stringify(document, null, 2)}\n`, { mode: 0o600 });
  }
  return added;
}

const host: HostContribution = {
  id: "traecode",
  displayName: "TraeCode",
  detection: { executable: "trae", configDirectory: ".trae-cn" },
  locations: {
    // The edition probe resolves the real dot-directory at write time; the
    // declared candidate covers the CN default the probe falls back to.
    configFiles: [".trae-cn/hooks.json", ".trae/hooks.json"],
    configFormat: "json",
    // No MCP config file of ours: see the adapter comment. The declared
    // candidates name the files whose stale entries of ours are swept.
    mcpConfigFiles: [],
    mcpFormat: "json",
    skillsDirectory: ".trae-cn/skills",
  },

  async install(env: HostEnvironment, options: HostInstallOptions): Promise<HostOperationResult> {
    const dotDirectory: string = traecodeDotDirectory(env.home);
    const configPath: string = join(env.home, dotDirectory, "hooks.json");
    const messages: string[] = [];

    const hooks = await installLifecycleHooks({
      hostId: host.id,
      configPath,
      format: host.locations.configFormat,
      runtimeDirectory: env.runtimeDirectory,
    });
    if (!hooks.ok) {
      return { hostId: host.id, ok: false, messages: hooks.messages };
    }
    const verified = await verifyLifecycleHooks(
      configPath,
      hooks.expected,
      "json",
      [...hookContainerPath(host.id)],
    );
    messages.push(...hooks.messages, ...verified.messages);
    if (!verified.ok) {
      messages.push(
        `the hooks are NOT wired: ${configPath} does not let TraeCode run them. ` +
          `Fix the entry by hand, then re-run install.`,
      );
      return { hostId: host.id, ok: false, messages };
    }

    // A stale user-level MCP entry of ours is migrated away: per-repository
    // files are the only correct registration for this host.
    if (await removeTraecodeUserMcpEntry(env.home)) {
      messages.push(
        `a stale user-level MCP entry of ours was removed from ${traecodeUserMcpPath(env.home)}; ` +
          `per-repository .trae/mcp.json files register the server instead`,
      );
    }

    const added: string[] = await addSandboxRules(env.home, dotDirectory);
    if (added.length > 0) {
      messages.push(`sandbox readWrite rules added (${added.join(", ")})`);
    }

    // The gate state makes the step self-silencing: once on, nothing is said.
    if (options.enableWorkspaceMcp) {
      if (await enableTraecodeWorkspaceMcp(env.home)) {
        messages.push(
          `workspace MCP enabled in ${traecodeUserSettingsPath(env.home)}; ` +
            `restart Trae windows to pick it up`,
        );
      } else {
        messages.push(
          `could not update ${traecodeUserSettingsPath(env.home)} (comments or unreadable JSON); ` +
            `set "${TRAE_WORKSPACE_MCP_KEY}": true in Trae settings instead.`,
        );
      }
    } else {
      messages.push(
        `Trae ignores per-repository MCP configs until workspace MCP is enabled, which keeps ` +
          `the memory tools hidden. Re-run install with --enable-workspace-mcp, or enable it in ` +
          `Trae settings (search "enableWorkspaceMcp").`,
      );
    }

    const skill = await copyPackagedSkill(host.id, env.home);
    messages.push(...skill.messages);
    return { hostId: host.id, ok: true, messages };
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
    // The stale-entry sweep runs on uninstall too, so a hand-copied user-level
    // entry of ours does not survive the host.
    if (await removeTraecodeUserMcpEntry(env.home)) {
      messages.push(`a stale user-level MCP entry of ours was removed`);
    }
    const skill = await removeInstalledSkill(host.id, env.home);
    messages.push(...skill.messages);
    // The global workspace-MCP gate is deliberately left on: other MCP
    // configurations may already rely on it, and turning a global setting off
    // would be its own breaking change.
    return { hostId: host.id, ok: ok && skill.ok, messages };
  },
};

async function readOrNull(path: string): Promise<string | null> {
  try {
    return await readFile(path, "utf8");
  } catch {
    return null;
  }
}

function isObject(value: unknown): value is JsonDocument {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

export const traecode: HostContribution = host;
