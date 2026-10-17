import { describe, expect, it } from "vitest";
import {
  insertIntoJsoncArray,
  jsoncDocumentIsEmpty,
  removeFromJsoncArray,
  removeJsoncMember,
  upsertJsoncMember,
} from "../../src/write/jsonc-edit";
import { OWNERSHIP_MARKER } from "../../src/write/marker";

/**
 * The comment-preserving edit is tested over the input shapes a host config
 * actually has: with and without a trailing comma, on one line or several,
 * with CRLF line endings, and with comments before and after the edited
 * region.
 */

const entry = { command: `/home/tester/.wenyuan/${OWNERSHIP_MARKER}/hooks/session-start.mjs` };
const path = ["hooks", "session-start"];
const isOwned = (source: string): boolean => source.includes(OWNERSHIP_MARKER);

const shapes: { name: string; source: string }[] = [
  { name: "no trailing comma", source: '{\n  "model": "sonnet",\n  "hooks": {\n    "session-start": []\n  }\n}\n' },
  {
    name: "a trailing comma",
    source: '{\n  "model": "sonnet",\n  "hooks": {\n    "session-start": [\n      { "command": "other" },\n    ]\n  }\n}\n',
  },
  { name: "a single line", source: '{"hooks":{"session-start":[]}}' },
  {
    name: "comments before and after",
    source:
      '{\n  // the model\n  "model": "sonnet",\n  "hooks": {\n    "session-start": [\n      { "command": "other" } // keep me\n    ]\n  } /* trailing */\n}\n',
  },
  {
    name: "CRLF line endings",
    source: '{\r\n  "hooks": {\r\n    "session-start": []\r\n  }\r\n}\r\n',
  },
  { name: "an empty file", source: "" },
];

describe("inserting an entry", () => {
  it.each(shapes)("adds the entry and preserves $name", ({ source }) => {
    const edit = insertIntoJsoncArray(source, path, entry);
    expect(edit).not.toBeNull();
    expect(edit?.changed).toBe(true);
    const text: string = edit?.text ?? "";
    expect(text).toContain(OWNERSHIP_MARKER);
    if (source.includes("//")) {
      expect(text).toContain("// the model");
      expect(text).toContain("// keep me");
      expect(text).toContain("/* trailing */");
    }
    if (source.includes("other")) {
      expect(text).toContain("other");
    }
    if (source.includes("\r\n")) {
      expect(text).toContain("\r\n");
    }
    expect(removable(text)).toBe(true);
  });

  it("is idempotent: a second insert leaves exactly one entry of ours", () => {
    const first = insertIntoJsoncArray('{\n  "hooks": {\n    "session-start": []\n  }\n}\n', path, entry);
    const second = insertIntoJsoncArray(first?.text ?? "", path, entry);
    const hits: number = (second?.text ?? "").split(OWNERSHIP_MARKER).length - 1;
    expect(hits).toBe(1);
  });

  it("leaves an unparseable file alone by reporting null", () => {
    expect(insertIntoJsoncArray("{ this is not json ", path, entry)).toBeNull();
    expect(insertIntoJsoncArray("[1,2,3]", path, entry)).toBeNull();
  });
});

describe("removing an entry", () => {
  it.each(shapes.filter((shape) => shape.source !== ""))("removes only its own entry from $name", ({ source }) => {
    const withBoth: string = insertIntoJsoncArray(source, path, entry)?.text ?? source;
    const removed = removeFromJsoncArray(withBoth, path, isOwned);
    expect(removed?.changed).toBe(true);
    const text: string = removed?.text ?? "";
    expect(text).not.toContain(OWNERSHIP_MARKER);
    if (source.includes("other")) {
      expect(text).toContain("other");
    }
    if (source.includes("//")) {
      expect(text).toContain("// the model");
      expect(text).toContain("// keep me");
    }
  });

  it("deletes the container the removal emptied rather than leaving an empty array", () => {
    const source: string = '{\n  "model": "sonnet",\n  "hooks": {\n    "session-start": []\n  }\n}\n';
    const withEntry: string = insertIntoJsoncArray(source, path, entry)?.text ?? "";
    const removed = removeFromJsoncArray(withEntry, path, isOwned);
    expect(removed?.text).toContain('"model": "sonnet"');
    expect(removed?.text).not.toContain('"session-start"');
  });

  it("reports an empty document when the removal took everything", () => {
    const source: string = '{\n  "hooks": {}\n}\n';
    const withEntry: string = insertIntoJsoncArray(source, path, entry)?.text ?? "";
    const removed = removeFromJsoncArray(withEntry, path, isOwned);
    expect(jsoncDocumentIsEmpty(removed?.text ?? "")).toBe(true);
  });

  it("leaves a foreign entry in place", () => {
    const source: string = '{\n  "hooks": {\n    "session-start": [\n      { "command": "other" }\n    ]\n  }\n}\n';
    const removed = removeFromJsoncArray(source, path, isOwned);
    expect(removed?.text).toBe(source);
  });
});

describe("a named member", () => {
  const servers = ["mcpServers"];

  it("writes the member and replaces its own later", () => {
    const source: string = '{\n  "model": "sonnet"\n}\n';
    const first = upsertJsoncMember(
      source,
      servers,
      "wenyuan-memory",
      { command: `/home/tester/.wenyuan/${OWNERSHIP_MARKER}/mcp-server.mjs` },
      isOwned,
    );
    expect(first?.text).toContain('"wenyuan-memory"');
    expect(first?.text).toContain('"model": "sonnet"');

    const second = upsertJsoncMember(
      first?.text ?? "",
      servers,
      "wenyuan-memory",
      { command: `/home/tester/.wenyuan/${OWNERSHIP_MARKER}/mcp-server-v2.mjs` },
      isOwned,
    );
    expect(second?.text).toContain("mcp-server-v2.mjs");
    expect(second?.text).not.toContain("/mcp-server.mjs\"");
    expect((second?.text ?? "").split('"wenyuan-memory":').length - 1).toBe(1);
  });

  it("reports a conflict instead of overwriting a foreign member", () => {
    const source: string = '{\n  "mcpServers": {\n    "wenyuan-memory": { "command": "other" }\n  }\n}\n';
    const edit = upsertJsoncMember(source, servers, "wenyuan-memory", { command: "node" }, isOwned);
    expect(edit?.conflict).toBe(true);
    expect(edit?.text).toBe(source);
  });

  it("removes its own member and not a foreign one", () => {
    const source: string = '{\n  "mcpServers": {\n    "wenyuan-memory": { "command": "other" }\n  }\n}\n';
    const removal = removeJsoncMember(source, servers, "wenyuan-memory", isOwned);
    expect(removal?.conflict).toBe(true);
    expect(removal?.text).toBe(source);
    const owned: string = '{\n  "mcpServers": {\n    "wenyuan-memory": { "command": "/x/wenyuan-memory/mcp-server.mjs" }\n  }\n}\n';
    const gone = removeJsoncMember(owned, servers, "wenyuan-memory", isOwned);
    expect(gone?.changed).toBe(true);
    expect(gone?.text).not.toContain("wenyuan-memory");
  });

  it("appends into a foreign list on the same key rather than reporting a conflict", () => {
    const source: string = '{\n  "hooks": {\n    "session-start": [\n      { "command": "other" }\n    ]\n  }\n}\n';
    const edit = upsertJsoncMember(source, ["hooks"], "session-start", entry, isOwned);
    expect(edit?.conflict).toBeUndefined();
    expect(edit?.changed).toBe(true);
    const text: string = edit?.text ?? "";
    expect(text).toContain('"command": "other"');
    expect(text).toContain(OWNERSHIP_MARKER);
    expect(text.split(OWNERSHIP_MARKER).length - 1).toBe(1);
  });

  it("keeps the foreign entries and stays idempotent across a re-run", () => {
    const source: string = '{\n  "hooks": {\n    "session-start": [\n      { "command": "other" }\n    ]\n  }\n}\n';
    const first: string = upsertJsoncMember(source, ["hooks"], "session-start", entry, isOwned)?.text ?? "";
    const second: string = upsertJsoncMember(first, ["hooks"], "session-start", entry, isOwned)?.text ?? "";
    expect(second).toContain('"command": "other"');
    expect(second.split(OWNERSHIP_MARKER).length - 1).toBe(1);
  });

  it("strips only its own element from a foreign list, leaving the host's", () => {
    const source: string = '{\n  "hooks": {\n    "session-start": [\n      { "command": "other" }\n    ]\n  }\n}\n';
    const withOurs: string = upsertJsoncMember(source, ["hooks"], "session-start", entry, isOwned)?.text ?? "";
    const removed = removeJsoncMember(withOurs, ["hooks"], "session-start", isOwned);
    expect(removed?.changed).toBe(true);
    const text: string = removed?.text ?? "";
    expect(text).not.toContain(OWNERSHIP_MARKER);
    expect(text).toContain('"command": "other"');
    expect(text).toContain('"session-start"');
  });

  it("deletes the key when its list held only this package's entry", () => {
    const source: string = '{\n  "hooks": {\n    "session-start": []\n  }\n}\n';
    const withOurs: string = upsertJsoncMember(source, ["hooks"], "session-start", entry, isOwned)?.text ?? "";
    const removed = removeJsoncMember(withOurs, ["hooks"], "session-start", isOwned);
    expect(removed?.changed).toBe(true);
    expect(removed?.text).not.toContain('"session-start"');
  });
});

/** A file the edit produced has to still be a document the editor can read. */
function removable(text: string): boolean {
  return removeFromJsoncArray(text, path, isOwned) !== null;
}
