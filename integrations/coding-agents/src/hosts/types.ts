import { existsSync } from "node:fs";
import { join } from "node:path";
import type { ConfigFormat } from "../write/json-merge";

/**
 * What a registered host contributes, and where its state lives.
 *
 * A host declares three places — its config file, its MCP config location and
 * its skills directory — and nothing else in the host's state is touched. Each
 * host keeps its own native config format rather than being normalized into a
 * shape shared with the others.
 */

export type HostId = "claude-code" | "codex" | "codebuddy" | "dsh";

export interface HostLocations {
  /**
   * Candidate config locations, relative to the home directory. On install the
   * first one that already exists is wired, so no competing config file is
   * written beside the host's real one; on uninstall all of them are swept.
   */
  configFiles: readonly string[];
  configFormat: ConfigFormat;
  /** Candidate MCP config locations, in the same order. */
  mcpConfigFiles: readonly string[];
  mcpFormat: ConfigFormat;
  /** The host's own skills root; this package installs a directory inside it. */
  skillsDirectory: string;
}

/** Detection is deliberately cheap: the executable, or the config directory. */
export interface HostDetection {
  executable: string;
  configDirectory: string;
}

export interface HostEnvironment {
  home: string;
  /** The stable staged runtime every written command path points into. */
  runtimeDirectory: string;
}

export interface HostInstallOptions {
  /** Authorizes flipping the host's global workspace-MCP setting. */
  enableWorkspaceMcp: boolean;
  /** Deployment contract values the packaged runtime reads. */
  environment?: Record<string, string>;
}

export interface HostOperationResult {
  hostId: HostId;
  ok: boolean;
  /** Printed by the command layer; a host adapter never writes to the console. */
  messages: string[];
}

export interface HostContribution {
  id: HostId;
  displayName: string;
  detection: HostDetection;
  locations: HostLocations;
  /** The host's global workspace-MCP setting key, when the host has one. */
  workspaceMcpKey?: string;
  install(env: HostEnvironment, options: HostInstallOptions): Promise<HostOperationResult>;
  uninstall(env: HostEnvironment): Promise<HostOperationResult>;
  /** Returns null when the host can be wired, or the reason it cannot. */
  preflight?(env: HostEnvironment): Promise<string | null>;
}

/**
 * The config location to write: the first candidate that already exists.
 *
 * `null` means this package writes no hook config for the host at all — true of
 * a host whose wiring is a plugin of its own, and of a host with no config file
 * of this shape. Callers skip such a host rather than resolving `home` as a path.
 */
export function hostConfigPath(host: HostContribution, home: string): string | null {
  const existing: string | undefined = host.locations.configFiles.find((candidate) =>
    existsSync(join(home, candidate)),
  );
  const relative: string | undefined = existing ?? host.locations.configFiles[0];
  return relative === undefined ? null : join(home, relative);
}

/** Every candidate config location, for a sweep that leaves no duplicate. */
export function hostConfigPaths(host: HostContribution, home: string): string[] {
  return host.locations.configFiles.map((candidate) => join(home, candidate));
}

/** The MCP config location to write: the first candidate that already exists. */
export function hostMcpConfigPath(host: HostContribution, home: string): string {
  const existing: string | undefined = host.locations.mcpConfigFiles.find((candidate) =>
    existsSync(join(home, candidate)),
  );
  return join(home, existing ?? host.locations.mcpConfigFiles[0] ?? "");
}

export function hostMcpConfigPaths(host: HostContribution, home: string): string[] {
  return host.locations.mcpConfigFiles.map((candidate) => join(home, candidate));
}

/** True when the host keeps its MCP registration in a config file of ours. */
export function hostHasMcpConfigFile(host: HostContribution): boolean {
  return host.locations.mcpConfigFiles.length > 0;
}
