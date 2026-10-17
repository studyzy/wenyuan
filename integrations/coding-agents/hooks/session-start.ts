import { failOpen, hooksDisabled, parsePayload, readStdin } from "../src/runtime/hook-contract";
import { stageDeadlineMs } from "../src/runtime/deadline";
import { runSessionStart } from "../src/runtime/session-start-stage";
import { homeDirectory } from "../src/config/paths";
import { registrationFor, resolveHostId } from "../src/runtime/hook-host";

/**
 * The packaged session-start hook.
 *
 * A host launches it by absolute path and resolves nothing else, so it is
 * self-contained. It is a thin adapter: it honors the disable guard, reads the
 * host's lifecycle payload from standard input, runs the session-start stage
 * behind the shared fail-open wrapper, prints the stage's envelope when there
 * is one, and exits zero on every fail-open path.
 */

const STAGE = "session-start";

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
    const result = await runSessionStart({
      eventName: registrationFor(hostId, STAGE).event,
      query: "What should I know about this project and the user at the start of a session?",
      timeoutMs: stageDeadlineMs(hostId, STAGE),
      hostId,
      home: homeDirectory(),
    });
    if (result.output !== null) {
      process.stdout.write(`${JSON.stringify(result.output)}\n`);
    }
  });
  return 0;
}

process.exit(await main());