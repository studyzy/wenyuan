import { EMPTY_RESULT_BUDGET, RETRY_BUDGET, payloadString } from "./hook-contract";
import { contextOutput, type ContextOutput } from "./hook-output";
import { openMemoryClient, type MemoryClient } from "./memory-client";
import { readSessionState, writeSessionState, type SessionState } from "./hook-state";

/**
 * The before-prompt stage behavior.
 *
 * The hook fires as a user prompt is submitted. It first refuses to do any
 * work when the payload carries no prompt, so a continuation or a
 * non-submission firing costs nothing. Otherwise it retrieves the memories
 * relevant to the prompt and injects them once per session, within a bounded
 * retry budget and a bounded empty-result budget so a persistently failing or
 * persistently empty source stops being retried.
 */

export interface PrePromptResult {
  eventName: string;
  output: ContextOutput | null;
  /** True when this firing injected context. */
  injected: boolean;
}

export interface PrePromptOptions {
  eventName: string;
  /** The stable per-session state key, so a later firing sees this one. */
  sessionKey: string;
  /** The document identity recorded in the session state. */
  documentId: string;
  /** The host's parsed lifecycle payload. */
  payload: Record<string, unknown>;
  timeoutMs: number;
  client?: MemoryClient | null;
}

/**
 * Runs the before-prompt behavior. It returns immediately, before reading the
 * payload's service state or building a client, when there is no prompt.
 */
export async function runPrePrompt(options: PrePromptOptions): Promise<PrePromptResult> {
  const prompt: string | undefined = payloadString(
    options.payload,
    "prompt",
    "user_prompt",
    "userPrompt",
    "content",
  );
  if (prompt === undefined) {
    return { eventName: options.eventName, output: null, injected: false };
  }

  const state: SessionState = await readSessionState(options.sessionKey, options.documentId);
  if (state.injected) {
    return { eventName: options.eventName, output: null, injected: false };
  }
  if (state.attempts >= EMPTY_RESULT_BUDGET) {
    return { eventName: options.eventName, output: null, injected: false };
  }

  const client: MemoryClient | null =
    options.client !== undefined
      ? options.client
      : await openMemoryClient(options.timeoutMs).catch(() => null);
  if (client === null) {
    return { eventName: options.eventName, output: null, injected: false };
  }

  // The retry bound and the empty-result budget are both enforced: a
  // persistently failing source consumes the retry budget and a persistently
  // empty one consumes the empty-result budget, so neither adds latency on
  // every prompt forever. No inter-attempt delay is imposed, because the
  // spec leaves the backoff shape open and each attempt is already bounded by
  // the client's own timeout.
  let attempts: number = state.attempts;
  for (let attempt = 0; attempt < RETRY_BUDGET; attempt += 1) {
    const outcome = await client
      .recall(prompt)
      .catch(() => ({ ok: false as const, texts: [] as string[] }));
    if (outcome.ok && outcome.texts.length > 0) {
      const next: SessionState = { ...state, injected: true, attempts };
      await writeSessionState(options.sessionKey, next);
      const context: string = outcome.texts.map((text) => `- ${text}`).join("\n");
      return { eventName: options.eventName, output: contextOutput(options.eventName, context), injected: true };
    }
    attempts += 1;
    if (attempts >= EMPTY_RESULT_BUDGET) {
      break;
    }
  }

  // Nothing relevant came back: consume the empty-result budget and emit nothing
  // rather than an empty context block.
  await writeSessionState(options.sessionKey, { ...state, injected: false, attempts });
  return { eventName: options.eventName, output: null, injected: false };
}