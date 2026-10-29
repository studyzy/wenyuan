import {
  appendedTurns,
  conversationDocumentId,
  conversationFromStopPayload,
  fingerprintDocumentId,
  isAppendOf,
  type Conversation,
} from "./conversation";
import { openMemoryClient, type MemoryClient } from "./memory-client";
import { readSessionState, writeSessionState } from "./hook-state";

/**
 * The stop stage behavior.
 *
 * It writes the session's conversation back to memory under a stable per-session
 * document identity. The conversation itself comes from the host's transcript
 * when the payload carries no turns — a `Stop` payload normally does not.
 *
 * What keeps a session's writes from piling up, in order of importance:
 *
 * 1. **one document id per session** (`conversation:<session>`), so every
 *    write-back addresses the same document rather than minting a new one;
 * 2. **a retain cursor** in the session state: the turns already handed over,
 *    so a later firing sends only what is new;
 * 3. **`update_mode`**: `append` when the conversation grew, `replace` when it
 *    was rewritten (a resume, a compaction), so a rewrite never duplicates.
 *
 * Nothing is written to the hook's standard output on any path.
 */

export interface StopResult {
  /** True when a write to memory succeeded. */
  written: boolean;
  /** The stable document identity the conversation was written under. */
  documentId: string;
  /** Why nothing was written, when nothing was. */
  reason?: string;
}

export interface StopOptions {
  hostId: string;
  payload: Record<string, unknown>;
  timeoutMs: number;
  client?: MemoryClient | null;
  /** The per-session state key, so the retain cursor survives between firings. */
  sessionKey?: string;
  /** The turns a prior firing already handed over, when the caller tracks them. */
  previousTurns?: readonly string[];
  /** Injected conversation, for a caller that already read the transcript. */
  conversation?: Conversation;
}

/** The context the write-back sends, so extraction knows whose claim a line is. */
export const RETAIN_CONTEXT =
  "conversation between the user and you (the coding agent): user turns are the user's words and decisions, " +
  "assistant turns are yours. When an assistant turn says something is fixed, passes or works, record it as " +
  'the agent\'s claim ("the agent reported X fixed"), not as an established fact.';

/**
 * Runs the stop behavior. It never throws: an unreachable service or an error
 * becomes a not-written result, and the host session is left unaffected.
 */
export async function runStop(options: StopOptions): Promise<StopResult> {
  const conversation: Conversation =
    options.conversation ?? (await conversationFromStopPayload(options.payload));
  const documentId: string =
    conversation.sessionId !== undefined && conversation.sessionId.length > 0
      ? conversationDocumentId(options.hostId, conversation.sessionId)
      : fingerprintDocumentId(conversation.fingerprint);
  if (conversation.turns.length === 0) {
    return { written: false, documentId, reason: "the session has no conversation to write back yet" };
  }

  const client: MemoryClient | null =
    options.client !== undefined
      ? options.client
      : await openMemoryClient(options.timeoutMs).catch(() => null);
  if (client === null) {
    return { written: false, documentId, reason: "the memory service is not configured or not reachable" };
  }

  const previous: readonly string[] =
    options.previousTurns ?? (await cursorFor(options.sessionKey, documentId));
  const appended: boolean = isAppendOf(previous, conversation.turns);
  const content: string = composeConversation(previous, conversation.turns);
  if (content.trim() === "") {
    return { written: false, documentId, reason: "nothing new to write back" };
  }

  const outcome = await client
    .capture({
      content,
      document_id: documentId,
      update_mode: appended ? "append" : "replace",
      context: RETAIN_CONTEXT,
      tags: ["source:chat", `harness:${options.hostId}`],
      metadata: {
        source: "chat",
        harness: options.hostId,
        ref_id: documentId,
        ...(conversation.sessionId === undefined ? {} : { session_id: conversation.sessionId }),
      },
      timestamp: new Date().toISOString(),
    })
    .catch(() => ({ ok: false as const }));

  if (!outcome.ok) {
    return { written: false, documentId, reason: "the memory service refused the write-back" };
  }
  await recordCursor(options.sessionKey, conversation.turns);
  return { written: true, documentId };
}

/** The turns a prior firing of this session already handed over. */
async function cursorFor(sessionKey: string | undefined, documentId: string): Promise<readonly string[]> {
  if (sessionKey === undefined) {
    return [];
  }
  const state = await readSessionState(sessionKey, documentId);
  return state.retainedTurns;
}

/** Records what was handed over, so the next firing appends instead of resending. */
async function recordCursor(sessionKey: string | undefined, turns: readonly string[]): Promise<void> {
  if (sessionKey === undefined) {
    return;
  }
  const state = await readSessionState(sessionKey, "");
  await writeSessionState(sessionKey, { ...state, retainedTurns: [...turns] });
}

/**
 * The content written back for one stop firing.
 *
 * When the conversation grew, the newly appended turns are the content; when it
 * was rewritten, the whole conversation is. The caller passes the turns a prior
 * firing recorded, and the decision comes from comparing them, so a rewrite
 * replaces rather than duplicating what was stored. With no prior firing the
 * whole conversation is written.
 */
export function composeConversation(previous: readonly string[], current: readonly string[]): string {
  return appendedTurns(previous, current).join("\n");
}