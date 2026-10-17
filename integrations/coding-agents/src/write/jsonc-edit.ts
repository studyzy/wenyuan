/**
 * A minimal text edit for a host config that permits comments.
 *
 * Some hosts write JSON with comments, trailing commas and their own
 * indentation. Parsing such a file and serializing it back would strip all of
 * that, so this module edits the text instead: it locates the one container an
 * entry belongs in and inserts or removes just that entry. Anything it cannot
 * parse is left alone; the caller then skips the host rather than rewriting a
 * file it does not understand.
 *
 * The scanner it sits on lives in `jsonc-scan`, which only answers where
 * things are and never rewrites the file.
 */
import {
  arrayElements,
  buildMissingMember,
  closingPad,
  deleteMember,
  detectIndent,
  findMember,
  findRange,
  insertMember,
  lineEndingOf,
  lineIndentAt,
  nextSignificant,
  previousSignificant,
  rangeIsEmpty,
  rootObjectRange,
  type Member,
  type Range,
} from "./jsonc-scan";
import { OWNERSHIP_MARKER } from "./marker";

export interface JsoncEdit {
  text: string;
  changed: boolean;
  /** True when the entry that is already there is not ours, so it stays. */
  conflict?: boolean;
}

function unchanged(source: string): JsoncEdit {
  return { text: source, changed: false };
}

/**
 * Inserts one entry into the array a key path names, creating the containers
 * it needs. Returns null when the file cannot be parsed, in which case the
 * caller leaves it byte-identical.
 */
export function insertIntoJsoncArray(
  source: string,
  path: readonly string[],
  entry: unknown,
): JsoncEdit | null {
  // A re-run replaces the entry it already wrote instead of stacking a second
  // copy, which is what makes a repeated install leave the file as it was.
  const cleaned: JsoncEdit | null = removeFromJsoncArray(source, path, (elementSource) =>
    elementSource.includes(OWNERSHIP_MARKER),
  );
  if (cleaned === null) {
    return null;
  }
  return insertIntoCleanedArray(cleaned.text, path, entry);
}

function insertIntoCleanedArray(
  source: string,
  path: readonly string[],
  entry: unknown,
): JsoncEdit | null {
  const entryText: string = JSON.stringify(entry);
  const lineEnding: string = lineEndingOf(source);
  const indent: string = detectIndent(source);
  if (source.trim() === "") {
    const body: string = buildMissingMember(path, entryText, indent, lineEnding);
    return { text: `{\n${indent}${body}\n}\n`, changed: true };
  }
  const root: Range | null = rootObjectRange(source);
  if (!root) {
    return null;
  }
  let object: Range = root;
  for (let depth = 0; depth < path.length; depth += 1) {
    const key: string | undefined = path[depth];
    if (key === undefined) {
      return null;
    }
    const member: Member | null = findMember(source, object, key);
    if (!member) {
      const memberText: string = buildMissingMember(
        path.slice(depth),
        entryText,
        indent,
        lineEnding,
      );
      return { text: insertMember(source, object, memberText), changed: true };
    }
    if (depth < path.length - 1) {
      if (source[member.value.start] !== "{") {
        return null;
      }
      object = member.value;
      continue;
    }
    if (source[member.value.start] !== "[") {
      return null;
    }
    const elements: Range[] = arrayElements(source, member.value);
    const last: Range | undefined = elements[elements.length - 1];
    if (last) {
      const pad: string = lineIndentAt(source, last.start) ?? indent;
      const at: number = last.end;
      return {
        text: `${source.slice(0, at)},${lineEnding}${pad}${entryText}${source.slice(at)}`,
        changed: true,
      };
    }
    const pad: string = closingPad(source, member.value) + indent;
    const body: string = `${lineEnding}${pad}${entryText}${lineEnding}${closingPad(
      source,
      member.value,
    )}`;
    return {
      text: `${source.slice(0, member.value.start + 1)}${body}${source.slice(member.value.end - 1)}`,
      changed: true,
    };
  }
  return unchanged(source);
}

/**
 * Removes every entry of ours from the array a key path names, then deletes
 * the containers the removal emptied. Returns null when the file cannot be
 * parsed.
 */
export function removeFromJsoncArray(
  source: string,
  path: readonly string[],
  isOwned: (elementSource: string) => boolean,
): JsoncEdit | null {
  if (source.trim() === "") {
    return unchanged(source);
  }
  const container: Range | null = findRange(source, path);
  if (!container || source[container.start] !== "[") {
    return unchanged(source);
  }
  const owned: Range[] = arrayElements(source, container).filter((element) =>
    isOwned(source.slice(element.start, element.end)),
  );
  if (owned.length === 0) {
    return unchanged(source);
  }
  let text: string = source;
  for (let index = owned.length - 1; index >= 0; index -= 1) {
    const element: Range | undefined = owned[index];
    if (!element) {
      continue;
    }
    const span: Range = extendOverAdjacentComma(text, element);
    text = `${text.slice(0, span.start)}${text.slice(span.end)}`;
  }
  return { text: pruneEmptiedContainers(text, path), changed: true };
}

/**
 * Writes one named member into the object a key path names: an existing
 * member of ours is replaced in place, a missing member is created, and a
 * foreign member is left alone unless its value is a list, in which case the
 * host's own elements are kept and ours is appended after them.
 */
export function upsertJsoncMember(
  source: string,
  path: readonly string[],
  key: string,
  value: unknown,
  isOwned: (memberSource: string) => boolean,
): JsoncEdit | null {
  const memberText: string = `${JSON.stringify(key)}: ${JSON.stringify(value)}`;
  const lineEnding: string = lineEndingOf(source);
  const indent: string = detectIndent(source);
  if (source.trim() === "") {
    const body: string = buildNestedMember(path, memberText, indent, lineEnding);
    return { text: `{\n${indent}${body}\n}\n`, changed: true };
  }
  const root: Range | null = rootObjectRange(source);
  if (!root) {
    return null;
  }
  let object: Range = root;
  for (let depth = 0; depth < path.length; depth += 1) {
    const step: string | undefined = path[depth];
    if (step === undefined) {
      return null;
    }
    const member: Member | null = findMember(source, object, step);
    if (!member) {
      const nested: string = buildNestedMember(
        path.slice(depth),
        memberText,
        indent,
        lineEnding,
      );
      return { text: insertMember(source, object, nested), changed: true };
    }
    if (source[member.value.start] !== "{") {
      return null;
    }
    object = member.value;
  }
  const existing: Member | null = findMember(source, object, key);
  if (existing) {
    if (source[existing.value.start] === "[") {
      // A list on this key is the host's own shape or one install already
      // appended to: strip any of ours and append ours after the host's
      // entries, so both survive and a re-run does not stack copies.
      const cleaned: string = withoutOwnElements(source, existing.value, isOwned);
      const elements: Range[] = arrayElements(cleaned, existing.value);
      if (elements.length === 0) {
        return {
          text: `${cleaned.slice(0, existing.value.start + 1)}${JSON.stringify(value)}${cleaned.slice(
            existing.value.end - 1,
          )}`,
          changed: true,
        };
      }
      const last: Range | undefined = elements[elements.length - 1];
      if (!last) {
        return { ...unchanged(source), conflict: true };
      }
      const pad: string = lineIndentAt(cleaned, last.start) ?? indent;
      const at: number = last.end;
      return {
        text: `${cleaned.slice(0, at)},${lineEnding}${pad}${JSON.stringify(value)}${cleaned.slice(at)}`,
        changed: true,
      };
    }
    // Ownership is decided by the value alone: the key is this package's own
    // name, so it carries the marker whether the entry is ours or not.
    if (!isOwned(source.slice(existing.value.start, existing.value.end))) {
      return { ...unchanged(source), conflict: true };
    }
    return {
      text: `${source.slice(0, existing.keyStart)}${memberText}${source.slice(existing.value.end)}`,
      changed: true,
    };
  }
  return { text: insertMember(source, object, memberText), changed: true };
}

/** Drops this package's own elements from one array, leaving the array's own. */
function withoutOwnElements(
  source: string,
  array: Range,
  isOwned: (elementSource: string) => boolean,
): string {
  const owned: Range[] = arrayElements(source, array).filter((element) =>
    isOwned(source.slice(element.start, element.end)),
  );
  let text: string = source;
  for (let index = owned.length - 1; index >= 0; index -= 1) {
    const element: Range | undefined = owned[index];
    if (!element) {
      continue;
    }
    const span: Range = extendOverAdjacentComma(text, element);
    text = `${text.slice(0, span.start)}${text.slice(span.end)}`;
  }
  return text;
}

/**
 * Removes one named member when it is ours, and when the host wrote a list on
 * the key removes only our elements from it, leaving the host's own behind.
 */
export function removeJsoncMember(
  source: string,
  path: readonly string[],
  key: string,
  isOwned: (memberSource: string) => boolean,
): JsoncEdit | null {
  if (source.trim() === "") {
    return unchanged(source);
  }
  const container: Range | null = findRange(source, path);
  if (!container || source[container.start] !== "{") {
    return unchanged(source);
  }
  const member: Member | null = findMember(source, container, key);
  if (!member) {
    return unchanged(source);
  }
  if (source[member.value.start] === "[") {
    // The host wrote a list on this key and install appended our entry to it:
    // strip only our elements, keep the host's own, and drop the key only when
    // nothing but our entries was ever there.
    const owned: Range[] = arrayElements(source, member.value).filter((element) =>
      isOwned(source.slice(element.start, element.end)),
    );
    if (owned.length === 0) {
      return { ...unchanged(source), conflict: true };
    }
    const cleaned: string = withoutOwnElements(source, member.value, isOwned);
    if (arrayElements(cleaned, member.value).length === 0) {
      const withoutMember: string | null = deleteMember(cleaned, [...path, key]);
      if (withoutMember === null) {
        return unchanged(source);
      }
      return { text: pruneEmptiedContainers(withoutMember, path), changed: true };
    }
    return { text: cleaned, changed: true };
  }
  if (!isOwned(source.slice(member.value.start, member.value.end))) {
    return { ...unchanged(source), conflict: true };
  }
  const withoutMember: string | null = deleteMember(source, [...path, key]);
  if (withoutMember === null) {
    return unchanged(source);
  }
  return { text: pruneEmptiedContainers(withoutMember, path), changed: true };
}

/** True when the edited document holds no members at all, so the file is a husk. */
export function jsoncDocumentIsEmpty(source: string): boolean {
  const root: Range | null = rootObjectRange(source);
  return root ? rangeIsEmpty(source, root) : false;
}

/** Grows a removal range over one comma beside it, never over content. */
function extendOverAdjacentComma(text: string, element: Range): Range {
  const after: number = nextSignificant(text, element.end);
  if (text[after] === ",") {
    return { start: element.start, end: after + 1 };
  }
  const before: number = previousSignificant(text, element.start);
  if (before >= 0 && text[before] === ",") {
    return { start: before, end: element.end };
  }
  return element;
}

/** Climbs out of a removal, dropping each container it emptied. */
function pruneEmptiedContainers(text: string, path: readonly string[]): string {
  let current: string = text;
  for (let depth = path.length; depth >= 1; depth -= 1) {
    const range: Range | null = findRange(current, path.slice(0, depth));
    if (!range || !rangeIsEmpty(current, range)) {
      break;
    }
    const next: string | null = deleteMember(current, path.slice(0, depth));
    if (next === null) {
      break;
    }
    current = next;
  }
  return current;
}

/** The member text for nested objects that do not exist yet. */
function buildNestedMember(
  keys: readonly string[],
  memberText: string,
  indent: string,
  lineEnding: string,
  depth = 0,
): string {
  const [head, ...rest] = keys;
  if (head === undefined) {
    return memberText;
  }
  const pad: string = indent.repeat(depth);
  const innerPad: string = indent.repeat(depth + 1);
  const inner: string =
    rest.length === 0
      ? memberText
      : buildNestedMember(rest, memberText, indent, lineEnding, depth + 1);
  return `${JSON.stringify(head)}: {${lineEnding}${innerPad}${inner}${lineEnding}${pad}}`;
}
