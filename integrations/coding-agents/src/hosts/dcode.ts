import { existsSync } from "node:fs";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { delimiter, dirname, join } from "node:path";
import { runDcodePlugin, DCODE_MARKETPLACE, DCODE_PLUGIN_ID } from "./dcode-plugin";
import { PER_USER_DIRECTORY_NAME, RUNTIME_DIRECTORY_NAME } from "../runtime/layout";
import type {
  HostContribution,
  HostEnvironment,
  HostInstallOptions,
  HostOperationResult,
} from "./types";

/**
 * Dcode (DeepAgents): a host whose memory wiring is its own native Agent
 * Plugin, so this installer registers the staged runtime as a local plugin
 * through Dcode's own plugin manager and writes no hook config at all.
 *
 * Dcode's plugin manager owns the cache, the copy and the enablement state, so
 * the adapter stays a thin CLI invocation. The plugin source points at the
 * staged runtime directory - the copy Dcode installs from is the same one every
 * hook command names - and a local marketplace file is prepared beside the
 * package's per-user state so the registration needs no network. Registering
 * command hooks beside the plugin would recall and retain twice, which is
 * worse than either alone.
 */

/** The marketplace file Dcode records the local plugin under. */
export const DCODE_MARKETPLACE_RELATIVE_PATH = ".agents/plugins/marketplace.json";

/** The fallback marketplace file, used when the conventional one is foreign. */
export const DCODE_FALLBACK_MARKETPLACE = "wenyuan-coding-agents-marketplace.json";

/**
 * Prepares a local marketplace without taking ownership of a foreign manifest
 * at the conventional path: a manifest that exists, parses, and is not ours is
 * left exactly as it is and the registration falls back to a file of our own.
 * Returns the directory the registration names, or null when neither file can
 * be written.
 */
export async function prepareDcodeMarketplace(home: string): Promise<string | null> {
  const root: string = join(home, PER_USER_DIRECTORY_NAME);
  const conventionalPath: string = join(root, DCODE_MARKETPLACE_RELATIVE_PATH);
  let path: string = conventionalPath;
  let marketplace: { name?: string; plugins?: unknown[] } = { plugins: [] };
  let conventionalUsable = true;
  if (existsSync(conventionalPath)) {
    let parsed: unknown;
    try {
      parsed = JSON.parse(await readFile(conventionalPath, "utf8"));
    } catch {
      parsed = null;
    }
    const record: Record<string, unknown> | null =
      parsed !== null && typeof parsed === "object" && !Array.isArray(parsed)
        ? (parsed as Record<string, unknown>)
        : null;
    if (record === null || !Array.isArray(record["plugins"])) {
      conventionalUsable = false;
    } else if (typeof record["name"] === "string" && record["name"] !== DCODE_MARKETPLACE) {
      // Do not mutate the identity of a marketplace another plugin may use.
      conventionalUsable = false;
    } else {
      marketplace = record as typeof marketplace;
    }
  }
  if (!conventionalUsable) {
    path = join(root, DCODE_FALLBACK_MARKETPLACE);
    if (existsSync(path)) {
      let parsed: unknown;
      try {
        parsed = JSON.parse(await readFile(path, "utf8"));
      } catch {
        parsed = null;
      }
      const record: Record<string, unknown> | null =
        parsed !== null && typeof parsed === "object" && !Array.isArray(parsed)
          ? (parsed as Record<string, unknown>)
          : null;
      if (record === null || record["name"] !== DCODE_MARKETPLACE || !Array.isArray(record["plugins"])) {
        return null;
      }
      marketplace = record as typeof marketplace;
    }
  }
  const foreign: unknown[] = marketplace.plugins ?? [];
  const kept: unknown[] = foreign.filter(
    (entry) =>
      entry === null || typeof entry !== "object" || (entry as Record<string, unknown>)["name"] !== DCODE_MARKETPLACE,
  );
  marketplace.name = DCODE_MARKETPLACE;
  marketplace.plugins = [
    ...kept,
    { name: DCODE_MARKETPLACE, source: { source: "local", path: `./${RUNTIME_DIRECTORY_NAME}` } },
  ];
  await mkdir(dirname(path), { recursive: true });
  await writeFile(path, `${JSON.stringify({ ...marketplace, plugins: marketplace.plugins }, null, 2)}\n`, {
    mode: 0o600,
  });
  return root;
}

const host: HostContribution = {
  id: "dcode",
  displayName: "Dcode",
  detection: { executable: "dcode", configDirectory: ".deepagents" },
  locations: {
    // The plugin owns Dcode's memory wiring: no hook config, no MCP server
    // map, and no skills mechanism to place a companion skill in.
    configFiles: [],
    configFormat: "json",
    mcpConfigFiles: [],
    mcpFormat: "json",
    skillsDirectory: "",
  },

  async preflight(_env: HostEnvironment): Promise<string | null> {
    const searchPath: string | undefined = process.env.PATH;
    const onPath: boolean =
      searchPath?.split(delimiter).some(
        (directory) => directory.length > 0 && existsSync(join(directory, "dcode")),
      ) ?? false;
    if (!onPath) {
      return "the dcode CLI is not on PATH";
    }
    return null;
  },

  async install(_env: HostEnvironment, _options: HostInstallOptions): Promise<HostOperationResult> {
    const source: string | null = await prepareDcodeMarketplace(homeDirectoryOf());
    if (source === null) {
      return {
        hostId: "dcode",
        ok: false,
        messages: ["the local plugin marketplace could not be prepared; refusing to register the plugin"],
      };
    }
    const added: boolean = await runDcodePlugin(["plugin", "marketplace", "add", source]);
    const installed: boolean = added && (await runDcodePlugin(["plugin", "install", DCODE_PLUGIN_ID]));
    const messages: string[] = [];
    if (installed) {
      messages.push(`the native Agent Plugin was installed (${DCODE_PLUGIN_ID})`);
      return { hostId: "dcode", ok: true, messages };
    }
    messages.push(
      `the native plugin could not be installed; run these by hand and re-run install:`,
      `  dcode plugin marketplace add "${source}" && dcode plugin install ${DCODE_PLUGIN_ID}`,
    );
    return { hostId: "dcode", ok: false, messages };
  },

  async uninstall(_env: HostEnvironment): Promise<HostOperationResult> {
    const messages: string[] = [];
    const removed: boolean = await runDcodePlugin(["plugin", "uninstall", DCODE_PLUGIN_ID]);
    // The marketplace we registered is retired too: `plugin uninstall` alone
    // leaves it listed and the plugin listed as disabled. Only ours is named
    // DCODE_MARKETPLACE, so a foreign marketplace is untouched.
    const marketplaceRemoved: boolean = await runDcodePlugin(["plugin", "marketplace", "remove", DCODE_MARKETPLACE]);
    if (removed) {
      messages.push("the native Agent Plugin was removed (foreign ~/.deepagents state preserved)");
      if (!marketplaceRemoved) {
        messages.push(
          `the ${DCODE_MARKETPLACE} marketplace is still registered; remove it with ` +
            `\`dcode plugin marketplace remove ${DCODE_MARKETPLACE}\``,
        );
      }
    } else {
      messages.push(`the native plugin could not be uninstalled (${DCODE_PLUGIN_ID})`);
    }
    return { hostId: "dcode", ok: removed, messages };
  },
};

function homeDirectoryOf(): string {
  return process.env.HOME ?? process.env.USERPROFILE ?? "";
}

export const dcode: HostContribution = host;
