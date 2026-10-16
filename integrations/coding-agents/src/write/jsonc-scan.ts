/**
 * The scanner behind the comment-preserving edit.
 *
 * It walks a host config's text without parsing it into a value, so comments,
 * trailing commas, the host's own indentation and its line endings all stay
 * exactly where they are. Everything exported here is a location primitive:
 * it answers where a container or a member is, and never rewrites the file.
 */

/** A half-open range of source text, `[start, end)`. */
export interface Range {
  start: number;
  end: number;
}

export interface Member {
  key: string;
  keyStart: number;
  value: Range;
}

export const lineEndingOf = (text: string): string => (text.includes("\r\n") ? "\r\n" : "\n");

export function isWhitespace(character: string | undefined): boolean {
  return character === " " || character === "\t" || character === "\n" || character === "\r";
}

/** Advances past whitespace and comments. */
export function skipInsignificant(text: string, index: number): number {
  let at: number = index;
  while (at < text.length) {
    const character: string | undefined = text[at];
    if (character === undefined) {
      break;
    }
    if (isWhitespace(character)) {
      at += 1;
      continue;
    }
    if (character === "/" && text[at + 1] === "/") {
      const newline: number = text.indexOf("\n", at);
      at = newline === -1 ? text.length : newline + 1;
      continue;
    }
    if (character === "/" && text[at + 1] === "*") {
      const close: number = text.indexOf("*/", at + 2);
      at = close === -1 ? text.length : close + 2;
      continue;
    }
    break;
  }
  return at;
}

export function nextSignificant(text: string, index: number): number {
  return skipInsignificant(text, index);
}

/**
 * The previous character that is not whitespace. A comment stops the walk:
 * this is only ever used to find a comma sitting directly beside a value.
 */
export function previousSignificant(text: string, index: number): number {
  let at: number = index - 1;
  while (at >= 0) {
    const character: string | undefined = text[at];
    if (character === undefined || !isWhitespace(character)) {
      return at;
    }
    at -= 1;
  }
  return -1;
}

/** Reads the string token starting at a quote, or null when it is unterminated. */
export function readStringToken(text: string, index: number): { value: string; end: number } | null {
  let at: number = index + 1;
  let value = "";
  while (at < text.length) {
    const character: string = text[at] ?? "";
    if (character === "\\") {
      value += text.slice(at, at + 2);
      at += 2;
      continue;
    }
    if (character === '"') {
      return { value, end: at + 1 };
    }
    value += character;
    at += 1;
  }
  return null;
}

/** The extent of the value starting at or after `index`. */
export function parseValueRange(text: string, index: number): Range | null {
  const start: number = skipInsignificant(text, index);
  const character: string | undefined = text[start];
  if (character === undefined) {
    return null;
  }
  if (character === '"') {
    const token: { value: string; end: number } | null = readStringToken(text, start);
    return token ? { start, end: token.end } : null;
  }
  if (character === "{" || character === "[") {
    const open: string = character;
    const close: string = open === "{" ? "}" : "]";
    let depth = 0;
    let at: number = start;
    while (at < text.length) {
      const current: string = text[at] ?? "";
      if (current === '"') {
        const token: { value: string; end: number } | null = readStringToken(text, at);
        if (!token) {
          return null;
        }
        at = token.end;
        continue;
      }
      if (current === "/" && text[at + 1] === "/") {
        const newline: number = text.indexOf("\n", at);
        at = newline === -1 ? text.length : newline + 1;
        continue;
      }
      if (current === "/" && text[at + 1] === "*") {
        const closeComment: number = text.indexOf("*/", at + 2);
        at = closeComment === -1 ? text.length : closeComment + 2;
        continue;
      }
      if (current === open) {
        depth += 1;
      } else if (current === close) {
        depth -= 1;
        if (depth === 0) {
          return { start, end: at + 1 };
        }
      }
      at += 1;
    }
    return null;
  }
  let at: number = start;
  while (at < text.length) {
    const current: string = text[at] ?? "";
    if (current === "," || current === "}" || current === "]" || isWhitespace(current)) {
      break;
    }
    if (current === "/" && (text[at + 1] === "/" || text[at + 1] === "*")) {
      break;
    }
    at += 1;
  }
  return at > start ? { start, end: at } : null;
}

export function rootObjectRange(text: string): Range | null {
  const start: number = skipInsignificant(text, 0);
  if (text[start] !== "{") {
    return null;
  }
  return parseValueRange(text, start);
}

/** Every member of an object, or null when it is not a plain object. */
export function objectMembers(text: string, object: Range): Member[] | null {
  if (text[object.start] !== "{") {
    return null;
  }
  const members: Member[] = [];
  let at: number = object.start + 1;
  while (at < object.end) {
    at = skipInsignificant(text, at);
    if (at >= object.end || text[at] === "}") {
      break;
    }
    if (text[at] !== '"') {
      return null;
    }
    const keyToken: { value: string; end: number } | null = readStringToken(text, at);
    if (!keyToken) {
      return null;
    }
    let cursor: number = skipInsignificant(text, keyToken.end);
    if (text[cursor] !== ":") {
      return null;
    }
    cursor = skipInsignificant(text, cursor + 1);
    const value: Range | null = parseValueRange(text, cursor);
    if (!value) {
      return null;
    }
    members.push({ key: keyToken.value, keyStart: at, value });
    const separator: number = skipInsignificant(text, value.end);
    if (text[separator] === ",") {
      at = separator + 1;
      continue;
    }
    break;
  }
  return members;
}

export function findMember(text: string, object: Range, key: string): Member | null {
  const members: Member[] | null = objectMembers(text, object);
  if (!members) {
    return null;
  }
  return members.find((member) => member.key === key) ?? null;
}

/** The range of the value a key path names, or null when any step is missing. */
export function findRange(text: string, path: readonly string[]): Range | null {
  const root: Range | null = rootObjectRange(text);
  if (!root) {
    return null;
  }
  if (path.length === 0) {
    return root;
  }
  let range: Range = root;
  for (const key of path) {
    const member: Member | null = findMember(text, range, key);
    if (!member) {
      return null;
    }
    range = member.value;
  }
  return range;
}

/** The elements of an array, or an empty list when it is not a plain array. */
export function arrayElements(text: string, array: Range): Range[] {
  if (text[array.start] !== "[") {
    return [];
  }
  const elements: Range[] = [];
  let at: number = array.start + 1;
  while (at < array.end) {
    at = skipInsignificant(text, at);
    if (at >= array.end || text[at] === "]") {
      break;
    }
    const value: Range | null = parseValueRange(text, at);
    if (!value) {
      return [];
    }
    elements.push(value);
    const separator: number = skipInsignificant(text, value.end);
    if (text[separator] === ",") {
      at = separator + 1;
      continue;
    }
    break;
  }
  return elements;
}

/** The indentation of the line holding `index`, when that line is blank so far. */
export function lineIndentAt(text: string, index: number): string | null {
  const previousLineEnd: number = Math.max(
    text.lastIndexOf("\n", index - 1),
    text.lastIndexOf("\r", index - 1),
  );
  const prefix: string = text.slice(previousLineEnd + 1, index);
  return /^[\t ]*$/.test(prefix) ? prefix : null;
}

/** The indentation the file uses, taken from its first indented line. */
export function detectIndent(text: string): string {
  const match: RegExpMatchArray | null = text.match(/^[ \t]+/m);
  return match ? match[0] : "  ";
}

/** The whitespace closing the line a container's closing bracket sits on. */
export function closingPad(text: string, container: Range): string {
  return lineIndentAt(text, container.end - 1) ?? "";
}

/** Serializes the member text for the containers an entry still needs. */
export function buildMissingMember(
  keys: readonly string[],
  entryText: string,
  indent: string,
  lineEnding: string,
  depth = 0,
): string {
  const [head, ...rest] = keys;
  if (head === undefined) {
    return entryText;
  }
  const pad: string = indent.repeat(depth);
  const innerPad: string = indent.repeat(depth + 1);
  if (rest.length === 0) {
    return `${JSON.stringify(head)}: [${lineEnding}${innerPad}${entryText}${lineEnding}${pad}]`;
  }
  return `${JSON.stringify(head)}: {${lineEnding}${innerPad}${buildMissingMember(
    rest,
    entryText,
    indent,
    lineEnding,
    depth + 1,
  )}${lineEnding}${pad}}`;
}

/** Inserts one member into an object, keeping the object's own formatting. */
export function insertMember(text: string, object: Range, memberText: string): string {
  const lineEnding: string = lineEndingOf(text);
  const indent: string = detectIndent(text);
  const members: Member[] | null = objectMembers(text, object);
  const last: Member | undefined = members?.[members.length - 1];
  if (members && members.length > 0 && last) {
    const pad: string = lineIndentAt(text, last.keyStart) ?? indent;
    const at: number = last.value.end;
    return `${text.slice(0, at)},${lineEnding}${pad}${memberText}${text.slice(at)}`;
  }
  const pad: string = closingPad(text, object) + indent;
  const body: string = `${lineEnding}${pad}${memberText}${lineEnding}${closingPad(text, object)}`;
  return `${text.slice(0, object.start + 1)}${body}${text.slice(object.end - 1)}`;
}

/** Deletes a member and exactly one comma beside it. */
export function deleteMember(text: string, path: readonly string[]): string | null {
  const parentPath: readonly string[] = path.slice(0, -1);
  const key: string | undefined = path[path.length - 1];
  if (key === undefined) {
    return null;
  }
  const object: Range | null =
    parentPath.length === 0 ? rootObjectRange(text) : findRange(text, parentPath);
  if (!object || text[object.start] !== "{") {
    return null;
  }
  const member: Member | null = findMember(text, object, key);
  if (!member) {
    return null;
  }
  let start: number = member.keyStart;
  let end: number = member.value.end;
  const after: number = nextSignificant(text, end);
  if (text[after] === ",") {
    end = after + 1;
  } else {
    const before: number = previousSignificant(text, start);
    if (before >= 0 && text[before] === ",") {
      start = before;
    }
  }
  return `${text.slice(0, start)}${text.slice(end)}`;
}

/** True when the range holds neither members nor elements. */
export function rangeIsEmpty(text: string, range: Range): boolean {
  if (text[range.start] === "[") {
    return arrayElements(text, range).length === 0;
  }
  if (text[range.start] === "{") {
    return (objectMembers(text, range) ?? []).length === 0;
  }
  return false;
}
