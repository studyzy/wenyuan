import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import {
  collectUsedKeys,
  flattenCatalog,
  icuArgs,
  resolveMessage,
  type UsedKey,
} from "./support/message-usage";

/**
 * The application resolves message keys at runtime, so a key that the catalog
 * does not define renders as the key path instead of a label. These suites pin
 * the catalog against the sources: every key the UI asks for must resolve, and
 * every message that carries ICU plurals must be used the way ICU expects.
 */

const MESSAGES_DIR = join(process.cwd(), "messages");

function loadEnglish(): Record<string, unknown> {
  return JSON.parse(readFileSync(join(MESSAGES_DIR, "en.json"), "utf8")) as Record<string, unknown>;
}

/** Collapse many call sites of one key into one report line. */
function describeKey(use: UsedKey): string {
  return `${use.key} (${use.file}:${use.line})`;
}

const used = collectUsedKeys();

describe("message catalog covers the sources", () => {
  it("finds translator call sites to check", () => {
    // A parser that silently stops matching would make the suites below vacuous.
    expect(used.length).toBeGreaterThan(100);
    expect(new Set(used.map((use) => use.file)).size).toBeGreaterThan(10);
  });

  it("resolves every key the application asks for", () => {
    const catalog = loadEnglish();
    const missing = used.filter((use) => resolveMessage(catalog, use.key) === undefined);
    // Report paths, not just names: the same key can be asked for from several views.
    expect(missing.map(describeKey)).toEqual([]);
  });

  it("resolves every namespace the application uses", () => {
    const catalog = loadEnglish();
    const namespaces = new Set(used.map((use) => use.key.split(".").slice(0, -1).join(".")));
    const missing = [...namespaces].filter((ns) => ns !== "" && resolveMessage(catalog, ns) === undefined);
    // A namespace is a catalog subtree; it resolves when the subtree has leaves.
    const absent = missing.filter((ns) => !flattenCatalog(catalog).some((key) => key.startsWith(`${ns}.`)));
    expect(absent).toEqual([]);
  });

  it("declares no dotted-key leaf that would shadow a nested branch", () => {
    const catalog = loadEnglish();
    const leaves = flattenCatalog(catalog);
    const branches = new Set<string>();
    for (const leaf of leaves) {
      const parts = leaf.split(".");
      for (let i = 1; i < parts.length; i += 1) branches.add(parts.slice(0, i).join("."));
    }
    expect(leaves.filter((leaf) => branches.has(leaf))).toEqual([]);
  });

  it("gives every ICU plural/select message a branch to render", () => {
    const catalog = loadEnglish();
    const problems: string[] = [];
    for (const key of new Set(used.map((use) => use.key))) {
      const message = resolveMessage(catalog, key);
      if (message === undefined) continue;
      const { emptyBranches } = icuArgs(message);
      for (const branch of emptyBranches) problems.push(`${key}: ${branch}`);
    }
    expect(problems).toEqual([]);
  });

  it("passes every ICU argument the message declares", () => {
    const catalog = loadEnglish();
    const problems: string[] = [];
    for (const use of used) {
      const message = resolveMessage(catalog, use.key);
      if (message === undefined) continue;
      for (const arg of icuArgs(message).args) {
        if (!use.args.includes(arg)) problems.push(`${describeKey(use)} needs {${arg}}`);
      }
    }
    // Reported per call site; a plural message used without its count renders the
    // plural syntax literally.
    expect([...new Set(problems)]).toEqual([]);
  });
});