import { mkdir, readFile, writeFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import { join } from "node:path";
import { wenyuanDirectory } from "../config/paths";

/**
 * The per-session progress the stage behaviors need.
 *
 * A later firing in the same session has to see what an earlier one did: the
 * before-prompt stage injects at most once per session and consumes a bounded
 * retry budget, and the stop stage writes a document whose identity is stable
 * for the session. The state is keyed by a stable per-session identity and
 * stored as one small JSON file per session under the per-user directory.
 *
 * Everything here is best-effort: a state that cannot be read is treated as
 * fresh, and a state that cannot be written never fails the hook.
 */

export interface SessionState {
  /** Whether the before-prompt stage has already injected this session. */
  injected: boolean;
  /** How many retrieval attempts have failed or come back empty. */
  attempts: number;
  /** The stable document identity the stop stage writes back under. */
  documentId: string;
}

const STATE_SUBDIRECTORY = "hook-state";

export function emptySessionState(documentId: string): SessionState {
  return { injected: false, attempts: 0, documentId };
}

/**
 * A stable per-session identity derived from the host and the session id the
 * payload carries, so the same session maps to the same state file across
 * firings and different sessions never share one.
 */
export function sessionKey(hostId: string, sessionId: string): string {
  return createHash("sha256").update(`${hostId}\u0000${sessionId}`).digest("hex");
}

/** The state file path for one session key. */
export function sessionStatePath(key: string): string {
  return join(wenyuanDirectory(), STATE_SUBDIRECTORY, `${key}.json`);
}

/** Reads the session state, treating a missing or broken file as fresh. */
export async function readSessionState(key: string, documentId: string): Promise<SessionState> {
  try {
    const source: string = await readFile(sessionStatePath(key), "utf8");
    const parsed: unknown = JSON.parse(source);
    if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
      return emptySessionState(documentId);
    }
    const record = parsed as Record<string, unknown>;
    return {
      injected: record["injected"] === true,
      attempts: typeof record["attempts"] === "number" ? record["attempts"] : 0,
      documentId: typeof record["documentId"] === "string" ? record["documentId"] : documentId,
    };
  } catch {
    return emptySessionState(documentId);
  }
}

/** Writes the session state, never failing the hook if it cannot be written. */
export async function writeSessionState(key: string, state: SessionState): Promise<void> {
  const path: string = sessionStatePath(key);
  try {
    await mkdir(join(wenyuanDirectory(), STATE_SUBDIRECTORY), { recursive: true });
    await writeFile(path, `${JSON.stringify(state)}\n`, { mode: 0o600 });
  } catch {
    // A hook that cannot record its progress still returns cleanly; the worst
    // case is one extra injection or write, never a failure to the host.
  }
}