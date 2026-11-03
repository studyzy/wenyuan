/** Small shared helpers, independent of any harness. */
import { accessSync, constants } from "node:fs";
import { delimiter, join } from "node:path";
import { entryIsOwned } from "../mcp/ownership";

/**
 * Is `bin` runnable? A path (contains "/") needs to exist and be executable; a bare name must be
 * found on PATH.
 *
 * Answered by inspecting the filesystem instead of spawning `which`: the callers use this to
 * DECIDE whether to launch something, and the lookup itself would be a process spawn on machines
 * where the answer is usually "no".
 */
export function binOnPath(bin: string): boolean {
  try {
    if (bin.includes("/")) {
      accessSync(bin, constants.X_OK);
      return true;
    }
    for (const dir of (process.env.PATH || "").split(delimiter)) {
      if (!dir) continue;
      try {
        accessSync(join(dir, bin), constants.X_OK);
        return true;
      } catch {
        /* keep scanning PATH */
      }
    }
    return false;
  } catch {
    return false;
  }
}

export const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/**
 * `version >= min`, comparing numeric major/minor/patch. A missing or unparseable version counts
 * as OLDER than everything: a capability probe has to fail closed, never assume support.
 * Pre-release and build suffixes ("0.9.0rc1", "0.9.0+dev") compare by their numeric part.
 */
export function semverGte(version: string | undefined, min: string): boolean {
  const parts = (v: string): [number, number, number] | undefined => {
    const m = v.trim().match(/^(\d+)\.(\d+)(?:\.(\d+))?/);
    return m ? [Number(m[1]), Number(m[2]), Number(m[3] ?? 0)] : undefined;
  };
  const a = version ? parts(version) : undefined;
  const b = parts(min);
  if (!a || !b) return false;
  const [a0, a1, a2] = a;
  const [b0, b1, b2] = b;
  if (a0 !== b0) return a0 > b0;
  if (a1 !== b1) return a1 > b1;
  return a2 >= b2;
}

/**
 * Bounded-concurrency map: run `fn` over `items` with at most `n` in flight. Item errors never
 * reject the pool — they go to `onError`, so one bad item cannot cancel the rest.
 */
export async function pool<T>(
  items: T[],
  n: number,
  fn: (x: T, i: number) => Promise<void>,
  onError?: (i: number, e: unknown) => void,
  onProgress?: (done: number, total: number) => void
): Promise<void> {
  let i = 0;
  let done = 0;
  async function worker(): Promise<void> {
    while (i < items.length) {
      const idx = i++;
      const item = items[idx];
      if (item === undefined) break;
      try {
        await fn(item, idx);
      } catch (e) {
        onError?.(idx, e);
      }
      onProgress?.(++done, items.length);
    }
  }
  await Promise.all(Array.from({ length: Math.min(n, items.length) }, worker));
}

/**
 * Is an existing MCP server entry THIS package's? Name ownership alone proves nothing — an
 * operator may register another server under our name — so the judgment is the entry's shape:
 * the node launcher plus the packaged server file inside this package's runtime directory, the
 * same rule install and uninstall agree on (`entryIsOwned`). An entry that merely mentions a
 * similar path is foreign and must be left alone.
 */
export function isOurMcpEntry(entry: unknown): boolean {
  return entryIsOwned(entry);
}
