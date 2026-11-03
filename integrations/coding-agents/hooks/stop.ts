import { readFileSync } from "node:fs";
import { parseHookEvent } from "../src/core/diag";
import { runHarnessRetain, type HookHarnessName } from "../src/harness/hook-lifecycle";
import { failOpen } from "../src/runtime/hook-contract";
import { harnessFor, resolveHostId } from "../src/runtime/hook-host";

/**
 * The packaged stop hook, launched when a session stops.
 *
 * A thin adapter over the core retain lifecycle: it reads the host's event
 * from standard input once, resolves which declared host is firing, and hands
 * event and harness to the shared core lifecycle, which owns the transcript
 * read, the retain cursor (append, never duplicate), the disable guard and
 * the fail-open behavior. Any failure exits zero.
 */

async function main(): Promise<number> {
  let raw: string;
  try {
    raw = readFileSync(0, "utf8");
  } catch {
    return 0;
  }
  const event = parseHookEvent("hook", raw) ?? {};
    const hostId: string | undefined = resolveHostId(event);
  if (hostId === undefined) {
    return 0;
  }
  let harness: HookHarnessName;
  try {
    harness = harnessFor(hostId);
  } catch (e) {
    return 0;
  }
  await failOpen(() => runHarnessRetain(harness, event));
  return 0;
}

process.exit(await main());
