import { failOpen, hooksDisabled, lifecyclePayload, payloadString, readStdin } from "../src/runtime/hook-contract";
import { stageDeadlineMs } from "../src/runtime/deadline";
import { runPrePrompt } from "../src/runtime/pre-prompt-stage";
import { conversationDocumentId } from "../src/runtime/conversation";
import { sessionKey } from "../src/runtime/hook-state";
import { registrationFor, resolveHostId } from "../src/runtime/hook-host";

/**
 * The packaged pre-prompt hook, launched before a user prompt is submitted.
 *
 * It is a thin adapter over the pre-prompt stage: it honors the disable guard,
 * reads the payload from standard input, runs the stage behind the shared
 * fail-open wrapper, prints the stage's context envelope when there is one, and
 * exits zero on every fail-open path.
 */

const STAGE = "pre-prompt";

async function main(): Promise<number> {
  if (hooksDisabled()) {
    return 0;
  }
  const source: string = await readStdin();
  const payload: Record<string, unknown> = lifecyclePayload(source);
  const hostId: string | undefined = resolveHostId(payload);
  if (hostId === undefined) {
    return 0;
  }
  const session: string = payloadString(payload, "session_id", "sessionId", "session") ?? "anonymous";
  await failOpen(async () => {
    const result = await runPrePrompt({
      eventName: registrationFor(hostId, STAGE).event,
      sessionKey: sessionKey(hostId, session),
      documentId: conversationDocumentId(hostId, session),
      payload,
      timeoutMs: stageDeadlineMs(hostId, STAGE),
    });
    if (result.output !== null) {
      process.stdout.write(`${JSON.stringify(result.output)}\n`);
    }
  });
  return 0;
}

process.exit(await main());