import { homedir } from "node:os";
import { join } from "node:path";

/**
 * Every per-user location this package uses, resolved lazily from the
 * environment so a test can point them at a temporary home simply by setting
 * the home variable, with no test-only branch anywhere in the installer.
 */
export const WENYUAN_DIRECTORY_NAME = ".wenyuan";
export const STORED_CONFIG_FILE_NAME = "config.json";
export const USAGE_LOG_FILE_NAME = "usage.jsonl";

/**
 * Deployment contract: containers and test harnesses set this name to
 * relocate the stored configuration file, so every read and every write
 * honors it.
 */
export const CONFIG_RELOCATION_ENV = "HINDSIGHT_CONFIG";

export function homeDirectory(): string {
  const fromEnvironment: string | undefined =
    process.env.HOME ?? process.env.USERPROFILE;
  if (fromEnvironment && fromEnvironment.length > 0) {
    return fromEnvironment;
  }
  return homedir();
}

export function wenyuanDirectory(): string {
  return join(homeDirectory(), WENYUAN_DIRECTORY_NAME);
}

/** The stored configuration's default location, before any relocation. */
export function defaultStoredConfigPath(): string {
  return join(wenyuanDirectory(), STORED_CONFIG_FILE_NAME);
}

/** The stored configuration's location, honoring the relocation variable. */
export function storedConfigPath(): string {
  const relocated: string | undefined = process.env[CONFIG_RELOCATION_ENV];
  if (relocated && relocated.length > 0) {
    return relocated;
  }
  return defaultStoredConfigPath();
}

export function storedConfigRelocated(): boolean {
  return storedConfigPath() !== defaultStoredConfigPath();
}

/** The append-only usage log the read-only command reports from. */
export function usageLogPath(): string {
  return join(wenyuanDirectory(), USAGE_LOG_FILE_NAME);
}
