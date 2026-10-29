import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";

/**
 * The conversation identity and turn extraction the stages share.
 *
 * Two sources feed a write-back. The cheap one is the hook payload itself, which
 * carries the turns on hosts that hand them over. The one that matters is the
 * host's transcript file: a `Stop` hook's payload is
 * `{session_id, transcript_path, cwd, hook_event_name, stop_hook_active}` and
 * carries no conversation at all, so a stop stage that only reads the payload
 * retains nothing on the host with the largest user base.
 *
 * Everything here is defensive: a transcript that is missing, half-written or in
 * a shape this reader does not know yields no turns rather than an error, so a
 * failed read costs one write-back and never the host's turn.
 */

export interface Conversation {
  /** A stable per-session identity, or undefined when the payload carries none. */
  sessionId: string | undefined;
  /** The turns, in order, each a plain text blob. */
  turns: string[];
  /** A fingerprint of the conversation's turns, sensitive to order and content. */
  fingerprint: string;
}

/**
 * The identity the stop stage writes a conversation back under.
 *
 * `conversation:<session>` is the reference implementation's own document id,
 * so a conversation re-read from disk on a later firing addresses the same
 * document instead of minting a new one.
 */
export function conversationDocumentId(hostId: string, sessionId: string): string {
  return `conversation:${sessionId.length > 0 ? sessionId : hash(`${hostId}\u0000${sessionId}`)}`;
}

/**
 * A stable identity for a conversation that arrived without a session id: the
 * content fingerprint, so the same conversation always addresses one document.
 */
export function fingerprintDocumentId(fingerprint: string): string {
  return `conversation:${fingerprint}`;
}

/** A fingerprint of the conversation's turns, sensitive to order and content. */
export function conversationFingerprint(turns: readonly string[]): string {
  const digest = createHash("sha256");
  for (const turn of turns) {
    digest.update(turn);
    digest.update("\u0000");
  }
  return digest.digest("hex");
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
  const turns: string[] = turnsFromPayload(payload);
  return {
    sessionId: firstString(payload, ["session_id", "sessionId", "session", "conversation_id"]),
    turns,
    fingerprint: conversationFingerprint(turns),
  };
}

/**
 * The conversation a stop firing should write back: the payload's own turns when
 * it carries any, otherwise the host's transcript.
 *
 * The transcript is read only when the payload came up empty, so a host that
 * hands the turns over pays nothing extra, and the path is taken from the
 * payload member every transcript-carrying host uses.
 */
export async function conversationFromStopPayload(
  payload: Record<string, unknown>,
  readTranscript: (path: string) => Promise<string[]> = readTranscriptTurns,
): Promise<Conversation> {
  const fromPayload = conversationFromPayload(payload);
  if (fromPayload.turns.length > 0) {
    return fromPayload;
  }
  const transcriptPath: string | undefined = firstString(payload, [
    "transcript_path",
    "transcriptPath",
    "conversation_path",
  ]);
  if (transcriptPath === undefined) {
    return fromPayload;
  }
  const turns: string[] = await readTranscript(transcriptPath).catch(() => []);
  return { ...fromPayload, turns, fingerprint: conversationFingerprint(turns) };
}

/**
 * Reads a host transcript file into plain turns: the user and assistant text,
 * and one action line per tool call.
 *
 * Transcripts differ per host, so the reader does not assume one schema. It
 * accepts the shapes in use — a JSON array of turns, a JSON object holding such
 * an array under a known key, and a JSONL stream (Claude Code's own
 * `{"type":"user"|"assistant","message":{…}}` records and the
 * `{"type":"message","role","content"}` records of the shared agent-cli
 * engine) — and takes whatever it can address, in order.
 */
export async function readTranscriptTurns(path: string): Promise<string[]> {
  const source: string = await readFile(path, "utf8");
  const trimmed: string = source.trim();
  if (trimmed === "") {
    return [];
  }
  if (trimmed.startsWith("[")) {
    const parsed: unknown = parseMaybe(trimmed);
    return Array.isArray(parsed) ? parsed.flatMap((record) => turnsOfRecord(record)) : [];
  }
  // A single JSON object: either a wrapper holding the message array, or one
  // record on its own. Anything else that starts with `{` is a JSONL stream
  // whose first line happens to start with `{` — a multi-line document that
  // fails to parse as one object must still be read line by line.
  const single: unknown = trimmed.startsWith("{") ? parseMaybe(trimmed) : undefined;
  if (single !== undefined && single !== null && typeof single === "object" && !Array.isArray(single)) {
    const record = single as Record<string, unknown>;
    const array: unknown = ["messages", "turns", "conversation", "history"]
      .map((key) => record[key])
      .find((value) => Array.isArray(value));
    if (Array.isArray(array)) {
      return array.flatMap((item) => turnsOfRecord(item));
    }
    const own: string[] = turnsOfRecord(record);
    if (own.length > 0) {
      return own;
    }
  }
  // A JSONL stream: a half-written last line is dropped rather than failing the
  // whole read, because a transcript is written while it is being read.
  const turns: string[] = [];
  for (const line of source.split("\n")) {
    const text: string = line.trim();
    if (text === "" || !text.startsWith("{")) {
      continue;
    }
    try {
      turns.push(...turnsOfRecord(JSON.parse(text)));
    } catch {
      continue;
    }
  }
  return turns;
}

/** The turns one transcript record contributes, or nothing when it is not a message. */
function turnsOfRecord(record: unknown): string[] {
  if (record === null || typeof record !== "object" || Array.isArray(record)) {
    return [];
  }
  const entry = record as Record<string, unknown>;
  // Claude Code's own record carries the role on `type` and the payload under
  // `message`; the shared agent-cli engine's carries `type: "message"` with a
  // top-level `role`.
  const role: string | undefined =
    typeof entry.role === "string"
      ? entry.role
      : entry.type === "user" || entry.type === "assistant"
        ? entry.type
        : undefined;
  if (role !== "user" && role !== "assistant") {
    return [];
  }
  const text: string = contentText(entry.message ?? entry);
  return text === "" ? [] : [`${role}: ${text}`];
}

/** The text of one message's content, whether it is a string or a block list. */
function contentText(content: unknown): string {
  if (typeof content === "string") {
    return content.trim();
  }
  if (content === null || typeof content !== "object") {
    return "";
  }
  const blocks: unknown = (content as Record<string, unknown>).content;
  if (typeof blocks === "string") {
    return blocks.trim();
  }
  if (!Array.isArray(blocks)) {
    return "";
  }
  const parts: string[] = [];
  for (const block of blocks) {
    if (typeof block === "string") {
      parts.push(block);
      continue;
    }
    if (block === null || typeof block !== "object") {
      continue;
    }
    const value = block as Record<string, unknown>;
    if (value.type === "text" && typeof value.text === "string") {
      parts.push(value.text);
    } else if (value.type === "tool_use" || value.type === "tool-call") {
      parts.push(`[action ${typeof value.name === "string" ? value.name : "tool"}]`);
    }
  }
  return parts.join("\n").trim();
}

/** Parses one JSON document, or undefined when it is not one. */
function parseMaybe(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
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

function hash(value: string): string {
  return createHash("sha256").update(value).digest("hex");
}