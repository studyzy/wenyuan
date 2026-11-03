import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readJsonl } from "../../../src/core/jsonl";

// Spy on the REAL readFileSync (not a stub that throws): the point is to prove nothing in this
// path calls it — past V8's maximum string length it throws ERR_STRING_TOO_LONG, and a reader
// that catches that reports "no turns" with no error anywhere.
vi.mock("node:fs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("node:fs")>();
  return { ...actual, readFileSync: vi.fn(actual.readFileSync) };
});

let root: string;
let file: string;

beforeEach(() => {
  root = mkdtempSync(join(tmpdir(), "wenyuan-jsonl-"));
  file = join(root, "transcript.jsonl");
  vi.mocked(readFileSync).mockClear();
});

afterEach(() => {
  rmSync(root, { recursive: true, force: true });
});

const read = () => [...readJsonl(file)];

describe("readJsonl", () => {
  it("yields every record", () => {
    writeFileSync(file, ["a", "b", "c"].join("\n") + "\n");
    expect(read()).toEqual(["a", "b", "c"]);
  });

  it("yields a final record that has no trailing newline", () => {
    writeFileSync(file, "a\nb");
    expect(read()).toEqual(["a", "b"]);
  });

  it("preserves blank and whitespace-only records for the caller to skip", () => {
    // The readers decide what to drop (they trim and skip empties); this must not silently change
    // the record stream they used to get from split("\n").
    writeFileSync(file, "a\n\n b \nc\n");
    expect(read()).toEqual(["a", "", " b ", "c"]);
  });

  it("keeps \r on CRLF records, exactly as split('\\n') did", () => {
    writeFileSync(file, "a\r\nb\r\n");
    expect(read()).toEqual(["a\r", "b\r"]);
  });

  it("reassembles a multi-byte character split across the read boundary", () => {
    // 'é' is 2 bytes; place it so its first byte ends one 64KB chunk and its second starts the next.
    const pad = "a".repeat(65535);
    writeFileSync(file, `${pad}é-tail\nsecond\n`);
    const lines = read();
    expect(lines[0]?.endsWith("é-tail")).toBe(true);
    expect(lines[0]).not.toContain("\uFFFD"); // no replacement char: the sequence survived
    expect(lines[1]).toBe("second");
  });

  it("yields nothing for a missing file instead of throwing", () => {
    expect(read()).toEqual([]);
  });

  it("yields nothing for an empty file", () => {
    writeFileSync(file, "");
    expect(read()).toEqual([]);
  });

  it("closes the file even when the consumer stops early", () => {
    writeFileSync(file, ["a", "b", "c"].join("\n") + "\n");
    const lines = readJsonl(file);
    for (const line of lines) {
      expect(line).toBe("a");
      break; // for..of calls the generator's return(), which must run the finally that closes the fd
    }
    expect(lines.next().done).toBe(true);
  });

  it("never reads the whole file into one string", () => {
    writeFileSync(file, "a\nb\n");
    expect(read()).toEqual(["a", "b"]);
    expect(vi.mocked(readFileSync)).not.toHaveBeenCalled();
  });
});
