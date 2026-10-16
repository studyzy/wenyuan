import { HOSTS, detectHosts, findHost, hostNames } from "./registry";
import type { HostContribution } from "./types";

/**
 * Host selection: the all-hosts keyword wires every host detected on this
 * machine, and one or more explicit names wire exactly those, bypassing
 * detection entirely so a host with no binary on the path and no config
 * directory yet is still wired.
 *
 * Both failures happen before anything is written.
 */

export const ALL_HOSTS_KEYWORD = "all";

export type SelectionResult =
  | { ok: true; hosts: HostContribution[]; detected: string[] }
  | { ok: false; message: string };

function acceptedHosts(): string {
  return hostNames().join(", ");
}

export function selectHosts(names: readonly string[], home: string): SelectionResult {
  if (names.length === 0) {
    return {
      ok: false,
      message:
        `no host was selected, and this package never guesses which coding agents to modify: ` +
        `pass "${ALL_HOSTS_KEYWORD}" to wire every detected host, or name one or more of: ${acceptedHosts()}.`,
    };
  }

  const unknown: string | undefined = names.find(
    (name) => name !== ALL_HOSTS_KEYWORD && !findHost(name),
  );
  if (unknown !== undefined) {
    return {
      ok: false,
      message: `unknown host "${unknown}"; accepted hosts: ${acceptedHosts()}.`,
    };
  }

  if (names.includes(ALL_HOSTS_KEYWORD)) {
    const detected: HostContribution[] = detectHosts(home);
    if (detected.length === 0) {
      return {
        ok: false,
        message:
          `no registered host was detected, so "${ALL_HOSTS_KEYWORD}" has nothing to wire: ` +
          `name one explicitly instead (${acceptedHosts()}).`,
      };
    }
    return { ok: true, hosts: detected, detected: detected.map((host) => host.id) };
  }

  // Registry order, so hosts are always wired in the order they are declared.
  const named: HostContribution[] = HOSTS.filter((host) => names.includes(host.id));
  return { ok: true, hosts: named, detected: [] };
}
