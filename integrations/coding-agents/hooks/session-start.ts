import { readFileSync } from "node:fs";
import { parseHookEvent } from "../src/core/diag";
import { runHarnessSessionStart, type HookHarnessName } from "../src/harness/hook-lifecycle";
import { failOpen } from "../src/runtime/hook-contract";
import { harnessFor, resolveHostId } from "../src/runtime/hook-host";

/**
 * The packaged session-start hook.
 *
 * A host launches it by absolute path and resolves nothing else, so it is a
 * thin adapter: it reads the host's event from standard input once, resolves
 * which declared host is firing, and hands event and harness to the shared
 * core lifecycle, which owns the disable guard, the output encoding and the
 * fail-open behavior. Any failure exits zero: a memoryless session beats a
 * broken one.
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
  } catch {
    return 0;
  }
  await failOpen(() => runHarnessSessionStart(harness, event));
  return 0;
}

process.exit(await main());
