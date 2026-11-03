import { existsSync } from "node:fs";
import { delimiter, join } from "node:path";
import { antigravityCli } from "./antigravity-cli";
import { claudeCode } from "./claude-code";
import { codebuddy } from "./codebuddy";
import { codex } from "./codex";
import { copilotCli } from "./copilot-cli";
import { cursorCli } from "./cursor-cli";
import { dcode } from "./dcode";
import { devinCli } from "./devin-cli";
import { dsh } from "./dsh";
import { factoryDroid } from "./factory-droid";
import { grokBuild } from "./grok-build";
import { kimi } from "./kimi";
import { qwenCode } from "./qwen-code";
import { traecode } from "./traecode";
import { workbuddy } from "./workbuddy";
import { zcode } from "./zcode";
import type { HostContribution, HostId } from "./types";

/**
 * The ordered registry of registered hosts.
 *
 * The list order is the order hosts are wired in. The registry and the install
 * machinery agree in both directions: every selectable host is installable, and
 * every installable host is selectable, so this list is the only place a host
 * is named.
 */

export const HOSTS: readonly HostContribution[] = [
  claudeCode,
  codex,
  codebuddy,
  workbuddy,
  cursorCli,
  copilotCli,
  dsh,
  antigravityCli,
  devinCli,
  grokBuild,
  qwenCode,
  factoryDroid,
  zcode,
  traecode,
  kimi,
  dcode,
];

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
  // A host that names no config directory is detected by its executable alone:
  // a directory it shares with another product must not read as this host.
  const configDirectory: string = host.detection.configDirectory;
  return (
    executableIsOnPath(host.detection.executable) ||
    (configDirectory.length > 0 && existsSync(join(home, configDirectory)))
  );
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
