import { appendedTurns, conversationFromPayload, conversationFingerprint, conversationDocumentId } from "./conversation";
import { openMemoryClient, type MemoryClient } from "./memory-client";

/**
 * The stop stage behavior.
 *
 * It writes the session's conversation back to memory under a stable
 * per-session document identity. When the conversation grew since the last
 * write its new turns are appended; when it was rewritten the stored content is
 * replaced. The decision is made from the conversation's content fingerprint,
 * so a rewrite never duplicates what was stored. Nothing is written to the
 * hook's standard output on any path.
 */

export interface StopResult {
  /** True when a write to memory succeeded. */
  written: boolean;
  /** The stable document identity the conversation was written under. */
  documentId: string;
}

export interface StopOptions {
  hostId: string;
  payload: Record<string, unknown>;
  timeoutMs: number;
  client?: MemoryClient | null;
  /** The previously stored content, when a prior firing recorded one. */
  previousTurns?: readonly string[];
}

/**
 * Runs the stop behavior. It never throws: an unreachable service or an error
 * becomes a not-written result, and the host session is left unaffected.
 */
export async function runStop(options: StopOptions): Promise<StopResult> {
  const conversation = conversationFromPayload(options.payload);
  const documentId: string =
    conversation.sessionId !== undefined
      ? conversationDocumentId(options.hostId, conversation.sessionId)
      : conversationDocumentId(options.hostId, conversationFingerprint(conversation.turns));
  if (conversation.turns.length === 0) {
    return { written: false, documentId };
  }

  const client: MemoryClient | null =
    options.client !== undefined
      ? options.client
      : await openMemoryClient(options.timeoutMs).catch(() => null);
  if (client === null) {
    return { written: false, documentId };
  }

  const previous: readonly string[] = options.previousTurns ?? [];
  const content: string = composeConversation(previous, conversation.turns);
  const outcome = await client
    .capture({ content })
    .catch(() => ({ ok: false as const }));
  return { written: outcome.ok, documentId };
}

/**
 * The content written back for one stop firing.
 *
 * When the conversation grew, the newly appended turns are the content; when it
 * was rewritten, the whole conversation is. The caller passes the turns a
 * prior firing recorded, and the decision comes from comparing them, so a
 * rewrite replaces rather than duplicating what was stored. With no prior
 * firing the whole conversation is written.
 */
export function composeConversation(previous: readonly string[], current: readonly string[]): string {
  return appendedTurns(previous, current).join("\n");
}