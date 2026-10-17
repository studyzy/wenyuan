import { stat } from "node:fs/promises";
import {
  hostConfigPath,
  hostMcpConfigPath,
  type HostContribution,
  type HostEnvironment,
} from "../hosts/types";

/**
 * The per-host blocking checks that run before anything is written.
 *
 * A check blocks only the host that fails it: the remaining hosts in the same
 * invocation are still wired, and the run reports which ones were not
 * installed. Detection is deliberately not part of this — a host with no
 * binary on the path and no config directory yet is still wired when named.
 */

export interface PreflightOutcome {
  host: HostContribution;
  /** null when the host can be wired, otherwise the reason it cannot. */
  reason: string | null;
}

export async function runPreflights(
  hosts: readonly HostContribution[],
  env: HostEnvironment,
): Promise<PreflightOutcome[]> {
  const outcomes: PreflightOutcome[] = [];
  for (const host of hosts) {
    outcomes.push(await runPreflight(host, env));
  }
  return outcomes;
}

export async function runPreflight(
  host: HostContribution,
  env: HostEnvironment,
): Promise<PreflightOutcome> {
  const configReason: string | null = await locationReason(hostConfigPath(host, env.home));
  if (configReason !== null) {
    return { host, reason: configReason };
  }
  // A host whose registration lives in its own store (the command-line host)
  // declares no MCP config location and has nothing to check here.
  if (host.locations.mcpConfigFiles.length > 0) {
    const mcpReason: string | null = await locationReason(hostMcpConfigPath(host, env.home));
    if (mcpReason !== null) {
      return { host, reason: mcpReason };
    }
  }
  const hostReason: string | null | undefined = await host.preflight?.(env);
  return { host, reason: hostReason ?? null };
}

/**
 * A location is fine when it is absent — every write creates its parent
 * directories — and fine when it is a regular file. Anything else, a directory
 * occupying the file's name for instance, can never be written.
 */
async function locationReason(path: string): Promise<string | null> {
  try {
    const info = await stat(path);
    return info.isFile()
      ? null
      : `${path} exists but is not a file, so this package cannot write it`;
  } catch (cause) {
    const code: string | undefined = (cause as NodeJS.ErrnoException).code;
    if (code === undefined || code === "ENOENT") {
      return null;
    }
    return `${path} cannot be read: ${code}`;
  }
}
