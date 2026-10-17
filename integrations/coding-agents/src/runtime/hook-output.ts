/**
 * The output envelopes the three hook stages emit to the host.
 *
 * A host reads a hook's standard output as either an empty stream or one JSON
 * object. The envelopes here are the ones this package writes: the session
 * start's message and context block, and the before-prompt context block. The
 * stop stage has no envelope because it stays silent on the success path.
 *
 * An envelope is only built when it carries at least one of its members: the
 * stages emit nothing rather than an empty object, so a read that finds no
 * content is unambiguously an absence.
 */

/** The model-facing context block the session-start and before-prompt stages add. */
export interface ContextOutput {
  hookSpecificOutput: {
    /** The host's own event spelling, carried so the host routes the block. */
    hookEventName: string;
    /** The context the host adds to the model's view, one string per entry. */
    additionalContext: string;
  };
}

/** The session start envelope: an optional user message and an optional context block. */
export interface SessionStartOutput extends Partial<ContextOutput> {
  /** A short user-facing line the host may surface in its session UI. */
  systemMessage?: string;
}

/**
 * The session-start envelope, or null when there is neither a message nor a
 * context block, so the caller emits nothing rather than an empty envelope.
 */
export function sessionStartOutput(
  eventName: string,
  message: string | undefined,
  context: string | undefined,
): SessionStartOutput | null {
  const output: SessionStartOutput = {};
  if (message !== undefined && message.length > 0) {
    output.systemMessage = message;
  }
  if (context !== undefined && context.length > 0) {
    output.hookSpecificOutput = { hookEventName: eventName, additionalContext: context };
  }
  return Object.keys(output).length > 0 ? output : null;
}

/** The before-prompt envelope, or null when there is no context to inject. */
export function contextOutput(eventName: string, context: string | undefined): ContextOutput | null {
  if (context === undefined || context.length === 0) {
    return null;
  }
  return { hookSpecificOutput: { hookEventName: eventName, additionalContext: context } };
}