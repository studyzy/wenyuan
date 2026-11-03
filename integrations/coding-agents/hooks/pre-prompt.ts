import { readFileSync } from "node:fs";
import { parseHookEvent } from "../src/core/diag";
import { runHarnessPrompt, type HookHarnessName } from "../src/harness/hook-lifecycle";
import { failOpen } from "../src/runtime/hook-contract";
import { harnessFor, resolveHostId } from "../src/runtime/hook-host";

/**
 * The packaged pre-prompt hook, launched before a user prompt is submitted.
 *
 * A thin adapter over the core prompt lifecycle: it reads the host's event
 * from standard input once, resolves which declared host is firing, and hands
 * event and harness to the shared core lifecycle, which owns the recall
 * injection, the disable guard, the output encoding and the fail-open
 * behavior. Any failure exits zero: a memoryless turn beats a broken one.
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
  await failOpen(() => runHarnessPrompt(harness, event));
  return 0;
}

process.exit(await main());
