import { existsSync } from "node:fs";
import { delimiter, join } from "node:path";
import { claudeCode } from "./claude-code";
import { codebuddy } from "./codebuddy";
import { codex } from "./codex";
import type { HostContribution, HostId } from "./types";

/**
 * The ordered registry of registered hosts.
 *
 * The first batch registers exactly these three. The registry and the install
 * machinery agree in both directions: every selectable host is installable, and
 * every installable host is selectable, so this list is the only place a host
 * is named.
 */

export const HOSTS: readonly HostContribution[] = [claudeCode, codex, codebuddy];

export const HOST_IDS: readonly HostId[] = HOSTS.map((host) => host.id);

export function hostNames(): string[] {
  return HOSTS.map((host) => host.id);
}

export function findHost(name: string): HostContribution | undefined {
  return HOSTS.find((host) => host.id === name);
}

export function isHostId(value: string): value is HostId {
  return HOSTS.some((host) => host.id === value);
}

/**
 * Detection is deliberately cheap and offline: the host's executable is on the
 * path, or the host's own config directory already exists.
 */
export function hostIsDetected(host: HostContribution, home: string): boolean {
  const configDirectory: string = join(home, host.detection.configDirectory);
  return executableIsOnPath(host.detection.executable) || existsSync(configDirectory);
}

export function detectHosts(home: string): HostContribution[] {
  return HOSTS.filter((host) => hostIsDetected(host, home));
}

function executableIsOnPath(name: string): boolean {
  const searchPath: string | undefined = process.env.PATH;
  if (!searchPath) {
    return false;
  }
  return searchPath
    .split(delimiter)
    .some(
      (directory) =>
        directory.length > 0 &&
        (existsSync(join(directory, name)) || existsSync(join(directory, `${name}.exe`))),
    );
}
