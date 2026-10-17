import { failOpen, hooksDisabled, parsePayload, readStdin } from "../src/runtime/hook-contract";
import { stageDeadlineMs } from "../src/runtime/deadline";
import { runStop } from "../src/runtime/stop-stage";
import { resolveHostId } from "../src/runtime/hook-host";

/**
 * The packaged stop hook, launched when a session stops.
 *
 * It is a thin adapter over the stop stage: it honors the disable guard, reads
 * the payload from standard input, runs the stage behind the shared fail-open
 * wrapper, and exits zero on every fail-open path. It writes nothing to its
 * standard output on any path, so the host session is never disturbed.
 */

const STAGE = "stop";

async function main(): Promise<number> {
  if (hooksDisabled()) {
    return 0;
  }
  const source: string = await readStdin();
  const payload: Record<string, unknown> = parsePayload(source);
  const hostId: string | undefined = resolveHostId(payload);
  if (hostId === undefined) {
    return 0;
  }
  await failOpen(async () => {
    await runStop({
      hostId,
      payload,
      timeoutMs: stageDeadlineMs(hostId, STAGE),
    });
  });
  return 0;
}

process.exit(await main());