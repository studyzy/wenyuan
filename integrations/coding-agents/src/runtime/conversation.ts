import { createHash } from "node:crypto";

/**
 * The conversation identity and content fingerprint the stages derive from the
 * host's lifecycle payload.
 *
 * The session identity is stable across every firing of one session, so the
 * stop stage writes back under one document and the before-prompt stage sees
 * what an earlier firing did. The content fingerprint changes whenever the
 * conversation's turns change, so the stop stage can tell an appended
 * conversation from a rewritten one.
 */

export interface Conversation {
  /** A stable per-session identity, or undefined when the payload carries none. */
  sessionId: string | undefined;
  /** The turns, in order, each a plain text blob. */
  turns: string[];
}

/** The identity the stop stage writes a conversation back under. */
export function conversationDocumentId(hostId: string, sessionId: string): string {
  return createHash("sha256").update(`${hostId}\u0000${sessionId}`).digest("hex");
}

/** A fingerprint of the conversation's turns, sensitive to order and content. */
export function conversationFingerprint(turns: readonly string[]): string {
  const hash = createHash("sha256");
  for (const turn of turns) {
    hash.update(turn);
    hash.update("\u0000");
  }
  return hash.digest("hex");
}

/**
 * True when `candidate` is `previous` with more turns appended, so the stop
 * stage appends the new turns rather than replacing the stored content. A
 * rewritten conversation shares no prefix and reports false.
 */
export function isAppendOf(previous: readonly string[], candidate: readonly string[]): boolean {
  if (candidate.length < previous.length) {
    return false;
  }
  for (let index = 0; index < previous.length; index += 1) {
    if (previous[index] !== candidate[index]) {
      return false;
    }
  }
  return candidate.length > previous.length;
}

/** The turns appended to `previous` to reach `candidate`, or all of `candidate`. */
export function appendedTurns(previous: readonly string[], candidate: readonly string[]): string[] {
  return isAppendOf(previous, candidate) ? candidate.slice(previous.length) : [...candidate];
}

/**
 * Extracts the conversation from a lifecycle payload. The host's payload shape
 * varies, so the reader looks for the members a payload may plausibly carry and
 * yields an empty conversation rather than failing when none is present.
 */
export function conversationFromPayload(payload: Record<string, unknown>): Conversation {
  return {
    sessionId: firstString(payload, ["session_id", "sessionId", "session"]),
    turns: turnsFromPayload(payload),
  };
}

function turnsFromPayload(payload: Record<string, unknown>): string[] {
  for (const key of ["turns", "messages", "conversation", "transcript"]) {
    const value: unknown = payload[key];
    if (Array.isArray(value)) {
      return value.map(turnText).filter((text): text is string => text !== null);
    }
  }
  const single: string | undefined = firstString(payload, ["prompt", "content", "text"]);
  return single === undefined ? [] : [single];
}

function turnText(turn: unknown): string | null {
  if (typeof turn === "string") {
    return turn.length > 0 ? turn : null;
  }
  if (turn !== null && typeof turn === "object" && !Array.isArray(turn)) {
    return firstString(turn as Record<string, unknown>, ["content", "text", "message"]) ?? null;
  }
  return null;
}

function firstString(source: Record<string, unknown>, keys: readonly string[]): string | undefined {
  for (const key of keys) {
    const value: unknown = source[key];
    if (typeof value === "string" && value.length > 0) {
      return value;
    }
  }
  return undefined;
}