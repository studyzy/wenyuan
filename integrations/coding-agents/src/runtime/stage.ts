import { statSync } from "node:fs";
import { chmod, cp, mkdir, readdir, rm, stat } from "node:fs/promises";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { runtimeDirectory } from "./layout";

/**
 * Staging: the built runtime is copied into the stable per-user directory
 * before any host is wired, so the very first hook command a host config
 * carries already points at the stable copy.
 *
 * The copy replaces the directory wholesale rather than merging into it, so
 * an entry point removed by a newer version cannot stay reachable.
 */

/** Where the bundler puts the runtime, relative to the package root. */
export const RUNTIME_SOURCE_SUBDIRECTORY = join("dist", "runtime");

export interface StagedRuntime {
  directory: string;
  files: string[];
}

export interface StageOptions {
  /** Defaults to the runtime shipped beside the running command. */
  source?: string;
}

async function directoryExists(path: string): Promise<boolean> {
  try {
    return (await stat(path)).isDirectory();
  } catch {
    return false;
  }
}

/**
 * The built runtime this command ships with. It is discovered by walking up
 * from the running file rather than assumed at a fixed depth, so the same
 * code works from a built CLI, from a bundled hook and from a source checkout
 * under the test runner.
 */
export function builtRuntimeDirectory(from: string = dirname(fileURLToPath(import.meta.url))): string | null {
  let current: string = resolve(from);
  for (let depth = 0; depth < 6; depth += 1) {
    const candidate: string = join(current, RUNTIME_SOURCE_SUBDIRECTORY);
    if (isDirectorySync(candidate)) {
      return candidate;
    }
    const parent: string = dirname(current);
    if (parent === current) {
      break;
    }
    current = parent;
  }
  return null;
}

function isDirectorySync(path: string): boolean {
  try {
    return statSync(path).isDirectory();
  } catch {
    return false;
  }
}

async function listFiles(directory: string): Promise<string[]> {
  const entries: string[] = [];
  async function walk(current: string): Promise<void> {
    for (const entry of await readdir(current, { withFileTypes: true })) {
      const path: string = join(current, entry.name);
      if (entry.isDirectory()) {
        await walk(path);
      } else {
        entries.push(relative(directory, path));
      }
    }
  }
  await walk(directory);
  return entries.sort();
}

/** A host execs a hook by path, so every staged entry point stays executable. */
async function makeExecutable(directory: string): Promise<void> {
  for (const file of await listFiles(directory)) {
    await chmod(join(directory, file), 0o755);
  }
}

/** Copies the built runtime into the stable directory, replacing it wholesale. */
export async function stageRuntime(options: StageOptions = {}): Promise<StagedRuntime> {
  const source: string | null = options.source ?? builtRuntimeDirectory();
  if (source === null || !(await directoryExists(source))) {
    throw new Error(
      `no built runtime found at ${RUNTIME_SOURCE_SUBDIRECTORY}; run the package's build script first`,
    );
  }
  const directory: string = runtimeDirectory();
  await rm(directory, { recursive: true, force: true });
  await mkdir(directory, { recursive: true });
  await cp(source, directory, { recursive: true });
  await makeExecutable(directory);
  return { directory, files: await listFiles(directory) };
}

/** True when a runtime has already been staged on this machine. */
export async function runtimeIsStaged(): Promise<boolean> {
  return directoryExists(runtimeDirectory());
}
