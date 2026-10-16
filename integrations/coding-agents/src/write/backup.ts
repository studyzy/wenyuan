import { copyFile, stat } from "node:fs/promises";
import { OWNERSHIP_MARKER } from "./marker";

/**
 * A backup is a sibling copy of the host config, named after the package so an
 * operator can tell who wrote it, and guarded on its own absence: the first
 * run captures the pre-install content and every later run leaves it alone.
 */
export const BACKUP_SUFFIX = `.${OWNERSHIP_MARKER}.bak`;

export function backupPathFor(filePath: string): string {
  return `${filePath}${BACKUP_SUFFIX}`;
}

async function exists(path: string): Promise<boolean> {
  try {
    await stat(path);
    return true;
  } catch {
    return false;
  }
}

/**
 * Copies a pre-existing file to its sibling backup and returns the backup's
 * path, or null when there was nothing to back up. Call this immediately
 * before the first content write to a file that already existed.
 */
export async function backupExistingFile(filePath: string): Promise<string | null> {
  if (!(await exists(filePath))) {
    return null;
  }
  const backupPath: string = backupPathFor(filePath);
  if (await exists(backupPath)) {
    return backupPath;
  }
  await copyFile(filePath, backupPath);
  return backupPath;
}
