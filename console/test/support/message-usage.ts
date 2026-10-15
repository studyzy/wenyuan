import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";

/**
 * Static extraction of message keys used by the UI.
 *
 * Views ask a namespace-scoped translator for a key, so a key in the source is
 * only resolvable once the namespace it belongs to is known. This module walks
 * the application sources, remembers every binding of a namespace-aware
 * translator (`useTranslations`, `getTranslations`, `useNamespacedMessages`),
 * and pairs each call on that binding with the namespace it was bound to.
 */

const SOURCE_ROOTS = ["app", "components", "lib"];

const TRANSLATOR_FACTORIES = [
  "useTranslations",
  "getTranslations",
  "useNamespacedMessages",
];

/** A key used at a call site, with the file it came from (for messages). */
export interface UsedKey {
  /** Full dotted key path, namespace included. */
  key: string;
  /** Bare key as written at the call site. */
  local: string;
  file: string;
  line: number;
  /** Values handed to the call, when they are a literal object. */
  args: string[];
}

function collectFiles(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir).sort()) {
    if (entry === "node_modules" || entry.startsWith(".")) continue;
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      collectFiles(full, out);
    } else if (/\.(ts|tsx)$/.test(entry)) {
      out.push(full);
    }
  }
  return out;
}

/**
 * Blank out comments, keeping offsets and line breaks intact so line numbers
 * stay accurate. Documentation that shows a call — `t("…")` in a doc comment —
 * must not read as a call site that the catalog then has to define.
 */
function stripComments(source: string): string {
  const out = source.split("");
  let i = 0;
  let quote: string | null = null;
  while (i < source.length) {
    const ch = source[i];
    const next = source[i + 1];
    if (quote !== null) {
      if (ch === "\\") i += 1;
      else if (ch === quote) quote = null;
    } else if (ch === '"' || ch === "'" || ch === "`") {
      quote = ch;
    } else if (ch === "/" && next === "/") {
      while (i < source.length && source[i] !== "\n") {
        out[i] = " ";
        i += 1;
      }
      continue;
    } else if (ch === "/" && next === "*") {
      while (i < source.length && !(source[i] === "*" && source[i + 1] === "/")) {
        if (source[i] !== "\n") out[i] = " ";
        i += 1;
      }
      out[i] = " ";
      out[i + 1] = " ";
      i += 1;
    }
    i += 1;
  }
  return out.join("");
}

/** Line number (1-based) of a character offset. */
function lineAt(source: string, index: number): number {
  let line = 1;
  for (let i = 0; i < index; i += 1) if (source[i] === "\n") line += 1;
  return line;
}

/** Identifiers of an inline object literal's keys, e.g. `f({ a, b: c })`. */
function literalArgNames(source: string, open: string): string[] {
  const end = open.indexOf(")");
  if (end === -1) return [];
  const body = open.slice(0, end);
  const names: string[] = [];
  const pattern = /([A-Za-z_$][\w$]*)\s*(?=[:,}]|$)/g;
  let match: RegExpExecArray | null;
  while ((match = pattern.exec(body)) !== null) names.push(match[1]);
  return names;
}

/**
 * Every namespace-aware translator call in the application sources.
 *
 * Calls whose key is not a string literal (template interpolation) are skipped:
 * the key is not knowable without running the code.
 */
export function collectUsedKeys(root = process.cwd()): UsedKey[] {
  const used: UsedKey[] = [];
  const files = SOURCE_ROOTS.flatMap((dir) => {
    try {
      return collectFiles(join(root, dir));
    } catch {
      return [];
    }
  });

  for (const file of files) {
    const source = stripComments(readFileSync(file, "utf8"));
    const rel = relative(root, file);
    const bindings: { name: string; namespace: string; index: number }[] = [];

    // `const t = useTranslations("ns")`, `const { t } = useTranslations("ns")`,
    // and the namespace-less `const t = useTranslations()`.
    const factory = new RegExp(
      String.raw`(?:const|let|var)\s+(?:\{\s*([\w$]+)\s*\}|([\w$]+))\s*=\s*(?:await\s+)?(?:${TRANSLATOR_FACTORIES.join("|")})\(\s*(?:(["'])([^"'\n]*)\3)?\s*\)`,
      "g",
    );
    let binding: RegExpExecArray | null;
    while ((binding = factory.exec(source)) !== null) {
      bindings.push({
        name: binding[1] ?? binding[2],
        namespace: binding[4] ?? "",
        index: binding.index,
      });
    }

    // One pass over every call on any translator name. A file binds the same
    // name several times (one namespace per section), so a call belongs to the
    // nearest binding above it — the one in scope where the call is written.
    const names = [...new Set(bindings.map((entry) => entry.name))];
    if (names.length > 0) {
      const call = new RegExp(
        String.raw`\b(${names.join("|")})(?:\.(?:rich|markup|raw))?\(\s*(["'])([^"'\n]*)\2\s*(,?\s*(\{)?)`,
        "g",
      );
      let hit: RegExpExecArray | null;
      while ((hit = call.exec(source)) !== null) {
        const owner = bindings
          .filter((entry) => entry.name === hit![1] && entry.index < hit!.index)
          .pop();
        if (owner === undefined) continue;
        const local = hit[3];
        const full = owner.namespace ? `${owner.namespace}.${local}` : local;
        const rest = source.slice(hit.index + hit[0].length);
        const args = hit[5] === "{" ? literalArgNames(rest, rest) : [];
        used.push({ key: full, local, file: rel, line: lineAt(source, hit.index), args });
      }
    }

    // Keys carried as data rather than looked up by a translator: server code
    // names the full path when it builds an error envelope.
    const dataKey = /(?:lookupMessage\(\s*[^,()]+,\s*|errorKey:\s*)(["'])([^"'\n]*)\1/g;
    let data: RegExpExecArray | null;
    while ((data = dataKey.exec(source)) !== null) {
      used.push({
        key: data[2],
        local: data[2],
        file: rel,
        line: lineAt(source, data.index),
        args: [],
      });
    }
  }

  return used;
}

/**
 * Resolve a dotted key against a nested catalog. Returns the message when the
 * path names a non-empty string, and `undefined` otherwise.
 */
export function resolveMessage(
  catalog: Record<string, unknown>,
  key: string,
): string | undefined {
  let cursor: unknown = catalog;
  for (const part of key.split(".")) {
    if (typeof cursor !== "object" || cursor === null) return undefined;
    cursor = (cursor as Record<string, unknown>)[part];
  }
  return typeof cursor === "string" && cursor.length > 0 ? cursor : undefined;
}

/** Every leaf path of a nested catalog. */
export function flattenCatalog(catalog: Record<string, unknown>, prefix = ""): string[] {
  return Object.entries(catalog).flatMap(([name, value]) => {
    const path = prefix ? `${prefix}.${name}` : name;
    return typeof value === "string" ? [path] : flattenCatalog(value as Record<string, unknown>, path);
  });
}

/**
 * Args of an ICU plural/select message body, plus whether every branch is
 * non-empty. A message without ICU plurals/selects reports no args.
 */
export function icuArgs(message: string): { args: string[]; emptyBranches: string[] } {
  const args: string[] = [];
  const emptyBranches: string[] = [];
  const opener = /\{([A-Za-z_$][\w$]*)\s*,\s*(plural|select|selectordinal)\s*,/g;
  let match: RegExpExecArray | null;
  while ((match = opener.exec(message)) !== null) {
    args.push(match[1]);
    // Walk the branches of this construct, tracking brace depth.
    let depth = 1;
    let i = opener.lastIndex;
    let branchStart = -1;
    while (i < message.length && depth > 0) {
      const ch = message[i];
      if (ch === "{") {
        depth += 1;
        if (depth === 2) branchStart = i + 1;
      } else if (ch === "}") {
        depth -= 1;
        if (depth === 1 && branchStart >= 0) {
          const branch = message.slice(branchStart, i);
          const body = branch.replace(/^[^{]*\{/, "");
          if (!body.replace(/[#\s{}]/g, "")) emptyBranches.push(`${match[1]}:${branch.slice(0, 24)}`);
          branchStart = -1;
        }
      }
      i += 1;
    }
  }
  return { args, emptyBranches };
}