import { openMemoryClient, type MemoryClient } from "./memory-client";
import { sessionStartOutput, type SessionStartOutput } from "./hook-output";

/**
 * The session-start stage behavior.
 *
 * It primes the session with context: it queries the memory service for what
 * is relevant to the session and, if the service is reachable and returns
 * anything, offers a context block and a short user-facing message. A session
 * that begins with no reachable service or no relevant memory emits nothing at
 * all rather than an empty envelope.
 */

export interface SessionStartResult {
  /** The event spelling the host routes the context block under. */
  eventName: string;
  output: SessionStartOutput | null;
}

export interface SessionStartOptions {
  eventName: string;
  /** A short line the host may surface; independent of the context block. */
  message?: string;
  /** The query used to prime the session; usually a fixed session opener. */
  query: string;
  /** The self-imposed deadline in milliseconds, passed to the client. */
  timeoutMs: number;
  /** Injectable for tests; a client is opened when absent. */
  client?: MemoryClient | null;
}

const DEFAULT_MESSAGE = "WenYuan memory is active for this session.";

/**
 * Runs the session-start behavior. It never throws: an unreachable service or
 * an error becomes a null context block, and the message is offered on its own.
 */
export async function runSessionStart(options: SessionStartOptions): Promise<SessionStartResult> {
  const client: MemoryClient | null =
    options.client !== undefined
      ? options.client
      : await openMemoryClient(options.timeoutMs).catch(() => null);

  let context: string | undefined;
  if (client !== null) {
    const outcome = await client.recall(options.query).catch(() => ({ ok: false as const, texts: [] }));
    if (outcome.ok && outcome.texts.length > 0) {
      context = composeContext(outcome.texts);
    }
  }

  const message: string = options.message ?? (context !== undefined ? DEFAULT_MESSAGE : "");
  return {
    eventName: options.eventName,
    output: sessionStartOutput(options.eventName, message, context),
  };
}

/** Joins the retrieved texts into one model-facing context block. */
export function composeContext(texts: readonly string[]): string {
  return texts.map((text) => `- ${text}`).join("\n");
}