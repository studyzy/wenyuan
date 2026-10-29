import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { asJsonDocument, type ConfigFormat } from "../write/json-merge";
import { findMember, objectMembers, rootObjectRange, type Member, type Range } from "../write/jsonc-scan";
import { HOOKS_CONTAINER_KEY, entriesHostWouldRun, hostReadsOurEntry } from "./hook-shape";
import type { HostHookExpectation } from "./hooks";

/**
 * The post-install read-back: what the host will actually load, read back out
 * of the file this package just wrote.
 *
 * Every failure this package can have on a wiring path — an event written in a
 * shape the host does not parse, a config file the host never reads, a foreign
 * entry occupying the key, a script that was staged but never named — looks
 * identical from the installer's side: the write succeeded. They are
 * distinguishable only by re-reading the host's file the way the host reads it
 * and checking that our entry is the one it would run.
 *
 * The check is deliberately narrow: it answers "would this host launch our hook
 * for this event", from the file and from the staged scripts, and nothing else.
 */

export interface HookVerification {
  ok: boolean;
  /** One line per event, in the order the host declares them. */
  messages: string[];
  /** The events the host would not run our hook for. */
  missing: string[];
}

/**
 * Reads one host's config back and checks every declared event names the
 * packaged script. A missing or unparseable file fails every event rather than
 * passing vacuously.
 */
export async function verifyLifecycleHooks(
  configPath: string,
  expected: readonly HostHookExpectation[],
  format: ConfigFormat,
): Promise<HookVerification> {
  const source: string | null = await readConfigSource(configPath);
  const events: Record<string, unknown> = source === null ? {} : eventsIn(source, format);
  const messages: string[] = [];
  const missing: string[] = [];
  for (const expectation of expected) {
    const value: unknown = events[expectation.event];
    if (hostReadsOurEntry(value, expectation.scriptPath, expectation.style)) {
      messages.push(`${expectation.event} → runs ${basename(expectation.scriptPath)}`);
      continue;
    }
    missing.push(expectation.event);
    messages.push(missingMessage(expectation, value));
  }
  return { ok: missing.length === 0, messages, missing };
}

/**
 * The events a host's config declares, read in the dialect the host uses.
 *
 * A host that permits comments is read with the comment-aware scanner: a plain
 * `JSON.parse` on such a file fails, and the check would then report every
 * event as missing even though the write was correct. This is why the reader
 * takes the format instead of assuming strict JSON.
 */
function eventsIn(source: string, format: ConfigFormat): Record<string, unknown> {
  if (format === "json") {
    const hooks: unknown = asJsonDocument(source)[HOOKS_CONTAINER_KEY];
    return isObject(hooks) ? (hooks as Record<string, unknown>) : {};
  }
  const root: Range | null = rootObjectRange(source);
  if (root === null) {
    return {};
  }
  const hooks: Member | null = findMember(source, root, HOOKS_CONTAINER_KEY);
  if (hooks === null || source[hooks.value.start] !== "{") {
    return {};
  }
  const events: Record<string, unknown> = {};
  for (const member of objectMembers(source, hooks.value) ?? []) {
    events[member.key] = JSON.parse(plainText(source, member.value));
  }
  return events;
}

/** One value of a comment-permitting document, as plain JSON. */
function plainText(source: string, range: Range): string {
  return stripComments(source.slice(range.start, range.end));
}

/**
 * Removes comments from one JSONC fragment. The scanner has already located the
 * value, so this only has to drop the comment spans that sit inside it.
 */
function stripComments(text: string): string {
  let out = "";
  let index = 0;
  let inString = false;
  while (index < text.length) {
    const character: string = text[index] ?? "";
    if (inString) {
      out += character;
      if (character === "\\") {
        out += text[index + 1] ?? "";
        index += 2;
        continue;
      }
      if (character === '"') {
        inString = false;
      }
      index += 1;
      continue;
    }
    if (character === '"') {
      inString = true;
      out += character;
      index += 1;
      continue;
    }
    if (character === "/" && text[index + 1] === "/") {
      const newline: number = text.indexOf("\n", index);
      index = newline === -1 ? text.length : newline;
      continue;
    }
    if (character === "/" && text[index + 1] === "*") {
      const close: number = text.indexOf("*/", index + 2);
      index = close === -1 ? text.length : close + 2;
      continue;
    }
    out += character;
    index += 1;
  }
  // A trailing comma before a closing bracket is legal in JSONC and not in JSON.
  return out.replace(/,\s*([}\]])/g, "$1");
}

/**
 * True when the host would run our hook for this event — the single question an
 * install has to answer before it may print "wired".
 */
export function eventWouldRun(value: unknown, expectation: HostHookExpectation): boolean {
  return hostReadsOurEntry(value, expectation.scriptPath, expectation.style);
}

/** The entries a host of this style would launch for one event, for reporting. */
export function entriesForReport(value: unknown, expectation: HostHookExpectation): string[] {
  return entriesHostWouldRun(value, expectation.style).map((entry) => entry.command);
}

/**
 * Why one event is not wired, in the operator's terms. A foreign entry and a
 * wrong shape need different fixes, so they are not reported the same way.
 */
function missingMessage(expectation: HostHookExpectation, value: unknown): string {
  const name: string = basename(expectation.scriptPath);
  if (value === undefined) {
    return `${expectation.event} → MISSING: the event is not in the file at all (${name} will never run)`;
  }
  const runnable: string[] = entriesForReport(value, expectation);
  if (runnable.length === 0) {
    return `${expectation.event} → WRONG SHAPE: the entry is not a ${expectation.style} this host parses (${name} will never run)`;
  }
  return (
    `${expectation.event} → NOT OURS: a foreign entry owns this event ` +
    `(${runnable.join(", ")}), so ${name} will never run`
  );
}

function basename(path: string): string {
  const index: number = path.lastIndexOf("/");
  return index === -1 ? path : path.slice(index + 1);
}

function isObject(value: unknown): boolean {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

async function readConfigSource(path: string): Promise<string | null> {
  try {
    return await readFile(path, "utf8");
  } catch {
    return null;
  }
}

/** The staged path one hook file resolves to, for a caller that has only its name. */
export function expectedScriptPath(runtimeDirectory: string, scriptFile: string): string {
  return join(runtimeDirectory, "hooks", scriptFile);
}