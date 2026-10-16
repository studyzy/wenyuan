/**
 * The operator-facing logging channel.
 *
 * A run prints one group per host plus one for the server configuration, so
 * the output reads as a sequence of hosts rather than an undifferentiated log.
 * Progress and warnings go to the ordinary output; failures go to the error
 * output. A host adapter never prints: it returns messages, and the command
 * layer that called it decides where they go.
 */

export function beginGroup(title: string): void {
  process.stdout.write(`\n[${title}]\n`);
}

export function say(message: string): void {
  process.stdout.write(`  ${message}\n`);
}

export function warn(message: string): void {
  process.stdout.write(`  warning: ${message}\n`);
}

export function fail(message: string): void {
  process.stderr.write(`  error: ${message}\n`);
}

export function printUsage(text: string): void {
  process.stdout.write(`${text}\n`);
}

/** Turns anything thrown into a line an operator can act on. */
export function describeError(cause: unknown): string {
  if (cause instanceof Error) {
    return cause.message;
  }
  return String(cause);
}
