/**
 * The cross-cutting contract every packaged hook shares.
 *
 * A hook runs inside a host process and is bounded by the budget the host
 * enforces, so nothing here may throw to the host, block on a terminal or
 * print on the success path. The contract gives each stage a guard that makes
 * every path a no-op, a fail-open wrapper that never lets a failure escape, the
 * bounded budgets the memory-facing stages share, and a reader for the host's
 * lifecycle payload on standard input.
 */

/**
 * The guard environment variable. This keeps its legacy deployment-contract
 * spelling because operators and CI already set it to disable the hooks. It is
 * spelled here, once, and every hook path consults it before it reads anything.
 */
export const DISABLE_HOOKS_ENV = "WENYUAN_DISABLE_HOOKS";

/** The truthy spellings the guard accepts; any other value leaves hooks on. */
const ENABLED_VALUES = new Set(["", "0", "false", "no", "off"]);

/**
 * True when hooks are disabled by the guard, in which case every hook path
 * becomes a no-op before it reads input, loads configuration or builds a
 * client.
 */
export function hooksDisabled(environment: NodeJS.ProcessEnv = process.env): boolean {
  const value: string | undefined = environment[DISABLE_HOOKS_ENV];
  if (value === undefined) {
    return false;
  }
  return !ENABLED_VALUES.has(value.trim().toLowerCase());
}

/** The bounded retrieval retry budget the before-prompt stage may consume. */
export const RETRY_BUDGET = 3;

/** The bounded empty-result budget after which retrieval stops being retried. */
export const EMPTY_RESULT_BUDGET = 2;

/**
 * The fail-open wrapper. It runs a stage body, and whatever the body does —
 * return a value, throw, or reject — the wrapper returns without throwing and
 * the hook exits zero. Nothing the body printed is emitted on the failure path.
 */
export async function failOpen<T>(
  body: () => Promise<T> | T,
  onFailure?: (reason: unknown) => void,
): Promise<{ ok: true; value: T } | { ok: false }> {
  try {
    return { ok: true, value: await body() };
  } catch (reason) {
    if (onFailure) {
      try {
        onFailure(reason);
      } catch {
        // A failing failure-handler must not become the failure it reports.
      }
    }
    return { ok: false };
  }
}

/** Reads all of standard input as text, never rejecting. */
export async function readStdin(stream: NodeJS.ReadableStream = process.stdin): Promise<string> {
  const chunks: Buffer[] = [];
  try {
    for await (const chunk of stream) {
      chunks.push(typeof chunk === "string" ? Buffer.from(chunk) : (chunk as Buffer));
    }
  } catch {
    return "";
  }
  return Buffer.concat(chunks).toString("utf8");
}

/**
 * Parses the host's lifecycle payload from standard input. A payload that is
 * absent, empty or not a JSON object becomes an empty object rather than an
 * error, so a stage always has a document to read from.
 */
export function parsePayload(source: string): Record<string, unknown> {
  const trimmed: string = source.trim();
  if (trimmed === "") {
    return {};
  }
  try {
    const parsed: unknown = JSON.parse(trimmed);
    return parsed !== null && typeof parsed === "object" && !Array.isArray(parsed)
      ? (parsed as Record<string, unknown>)
      : {};
  } catch {
    return {};
  }
}

/**
 * The environment members a host may export instead of sending a payload.
 *
 * DSH launches a hook with the lifecycle on the command's environment, not on
 * standard input, so the event it is firing and the host it fires for arrive as
 * two members. The payload keys are the ones the stages already read.
 */
const LIFECYCLE_ENV_MEMBERS: Readonly<Record<string, string>> = {
  WENYUAN_HOOK_EVENT: "hook_event_name",
  WENYUAN_HOOK_HOST: "host",
};

/**
 * The payload a hook reads: the standard input document with the environment
 * members folded in. A firing that carries neither yields an empty document, so
 * a stage still runs rather than failing.
 */
export function lifecyclePayload(
  source: string,
  environment: NodeJS.ProcessEnv = process.env,
): Record<string, unknown> {
  const payload: Record<string, unknown> = parsePayload(source);
  for (const [name, key] of Object.entries(LIFECYCLE_ENV_MEMBERS)) {
    const value: string | undefined = environment[name];
    if (value !== undefined && value.length > 0) {
      payload[key] = value;
    }
  }
  return payload;
}

/** Reads a string member of the payload, or undefined when it is not a string. */
export function payloadString(payload: Record<string, unknown>, ...keys: string[]): string | undefined {
  for (const key of keys) {
    const value: unknown = payload[key];
    if (typeof value === "string" && value.length > 0) {
      return value;
    }
  }
  return undefined;
}