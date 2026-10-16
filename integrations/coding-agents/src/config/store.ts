import { readJsonFile, writeJsonFile, type JsonDocument } from "../write/json-merge";
import { storedConfigPath } from "./paths";

/**
 * The single stored per-user configuration file. It is shared with the
 * packaged runtime that hosts actually execute, so this module reads and
 * writes the same file the runtime reads, through the same location.
 */

export type StoredConfig = JsonDocument;

/** Reads the stored configuration, treating a missing or broken file as empty. */
export async function readStoredConfig(): Promise<StoredConfig> {
  const { document } = await readJsonFile(storedConfigPath());
  return document;
}

/**
 * Merges a patch into the stored configuration and writes it owner-only.
 * Every unrelated key already present survives, and a member whose value is
 * `undefined` is removed rather than written as null.
 */
export async function writeStoredConfig(patch: StoredConfig): Promise<void> {
  const existing: StoredConfig = await readStoredConfig();
  const merged: StoredConfig = { ...existing };
  for (const [key, value] of Object.entries(patch)) {
    if (value === undefined) {
      delete merged[key];
    } else {
      merged[key] = value;
    }
  }
  await writeJsonFile(storedConfigPath(), merged);
}

/** True when a stored configuration file is already present. */
export async function storedConfigExists(): Promise<boolean> {
  const { existed } = await readJsonFile(storedConfigPath());
  return existed;
}
