import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { readTranscript } from "../../../src/core/transcript";
import { actionLine, stripInjectedMemory, truncate, TOOL_TEXT_CAP } from "../../../src/core/transcript-util";

let root: string;
let file: string;

beforeEach(() => {
  root = mkdtempSync(join(tmpdir(), "wenyuan-transcript-"));
  file = join(root, "session.jsonl");
});

afterEach(() => {
  rmSync(root, { recursive: true, force: true });
});

describe("readTranscript", () => {
  it("captures text + compact action turns, dropping tool_result/non-message/isMeta/isSidechain/thinking/empty lines and tolerating malformed or non-object JSON lines", () => {
    const lines = [
      // non-message line: dropped
      JSON.stringify({ type: "last-prompt", leafUuid: "x" }),
      // kept: string content
      JSON.stringify({
        type: "user",
        timestamp: "2026-01-01T00:00:00Z",
        message: { role: "user", content: "how do we validate input?" },
      }),
      // kept: only the text block survives (thinking dropped)
      JSON.stringify({
        type: "assistant",
        timestamp: "2026-01-01T00:00:01Z",
        message: {
          role: "assistant",
          content: [
            { type: "thinking", thinking: "hmm" },
            { type: "text", text: "We use zod." },
          ],
        },
      }),
      // kept: a tool_use-only assistant line becomes a compact role:"action" turn (name + target)
      JSON.stringify({
        type: "assistant",
        message: {
          role: "assistant",
          content: [{ type: "tool_use", name: "Bash", input: { command: "npm test" } }],
        },
      }),
      // dropped: a tool_result-only user line — outputs are mechanical noise for extraction
      JSON.stringify({
        type: "user",
        message: { role: "user", content: [{ type: "tool_result", content: "12 passed" }] },
      }),
      // dropped: isMeta
      JSON.stringify({
        type: "user",
        isMeta: true,
        message: { role: "user", content: "<system-injected>" },
      }),
      // dropped: isSidechain (subagent/Task turn, not the main conversation)
      JSON.stringify({
        type: "assistant",
        isSidechain: true,
        message: { role: "assistant", content: "subagent output" },
      }),
      // malformed line: must not throw
      "{ not json",
      // JSON.parse succeeds but yields a non-object value: must not throw
      "null",
      "42",
      "[]",
      // blank line: must be skipped
      "",
    ];
    writeFileSync(file, lines.join("\n"));

    const result = readTranscript(file);

    expect(result).toEqual([
      { role: "user", content: "how do we validate input?", timestamp: "2026-01-01T00:00:00Z" },
      { role: "assistant", content: "We use zod.", timestamp: "2026-01-01T00:00:01Z" },
      { role: "action", content: "Bash npm test" },
    ]);
  });

  it("splits a mixed text + tool_use message into a prose turn plus an action turn; drops the tool_result", () => {
    const lines = [
      JSON.stringify({
        type: "assistant",
        timestamp: "2026-01-01T00:00:02Z",
        message: {
          role: "assistant",
          content: [
            { type: "text", text: "Editing the uploader." },
            {
              type: "tool_use",
              name: "Edit",
              input: { file_path: "uploader.ts", old_string: "a" },
            },
          ],
        },
      }),
      JSON.stringify({
        type: "user",
        message: { role: "user", content: [{ type: "tool_result", content: "x".repeat(5000) }] },
      }),
    ];
    writeFileSync(file, lines.join("\n"));

    const result = readTranscript(file);

    expect(result).toEqual([
      { role: "assistant", content: "Editing the uploader.", timestamp: "2026-01-01T00:00:02Z" },
      // Action line: tool name + primary target only — no arguments, no output.
      { role: "action", content: "Edit uploader.ts", timestamp: "2026-01-01T00:00:02Z" },
    ]);
  });

  it("strips an injected <wenyuan_memories> block and keeps the user's own text around it", () => {
    writeFileSync(
      file,
      JSON.stringify({
        type: "user",
        message: {
          role: "user",
          content:
            "<wenyuan_memories>\nsecret prior fact\n</wenyuan_memories>\nWhy does upload retry?",
        },
      })
    );

    const result = readTranscript(file);

    expect(result).toHaveLength(1);
    expect(result[0]?.content).toContain("Why does upload retry?");
    expect(result[0]?.content).not.toContain("secret prior fact");
    expect(result[0]?.content).not.toContain("wenyuan_memories");
  });

  it("strips the reflect hook's <wenyuan_memory> injection block wholesale", () => {
    // The exact block shape the UserPromptSubmit hook injects — wrapper tags, preamble,
    // attribution text and the surfaced memory itself must ALL be gone from retained text, or the
    // session write-back would re-ingest the injected synthesis (a retain→reflect feedback loop).
    const injected = [
      "<wenyuan_memory>",
      "Relevant project memory:",
      "",
      "SECRET",
      "</wenyuan_memory>",
    ].join("\n");
    writeFileSync(
      file,
      JSON.stringify({
        type: "user",
        message: { role: "user", content: `${injected}\nWhy does upload retry?` },
      })
    );

    const result = readTranscript(file);

    expect(result).toHaveLength(1);
    expect(result[0]?.content).toContain("Why does upload retry?");
    expect(result[0]?.content).not.toContain("SECRET");
    expect(result[0]?.content).not.toContain("Relevant project memory");
    expect(result[0]?.content).not.toContain("wenyuan_memory");
  });

  it("drops the compaction summary — the host's recap, not the user's words", () => {
    // Written when the context window fills, as a plain type:"user" record with no isMeta flag.
    // Measured at 29 records / 474,016 chars across local transcripts, averaging 16KB each, every
    // one retained as if the user had typed it — and each one summarising turns already retained,
    // since compaction APPENDS to the transcript rather than rewriting it.
    writeFileSync(
      file,
      [
        JSON.stringify({
          type: "user",
          isCompactSummary: true,
          message: {
            role: "user",
            content:
              "This session is being continued from a previous conversation that ran out of " +
              "context. The summary below covers <analysis>…</analysis>",
          },
        }),
        JSON.stringify({
          type: "user",
          message: { role: "user", content: "now add the retry backoff" },
        }),
      ].join("\n")
    );

    const result = readTranscript(file);
    expect(result).toHaveLength(1);
    expect(result[0]?.content).toBe("now add the retry backoff");
  });

  it("drops a <task-notification>: the harness's background-task plumbing, not the user's words", () => {
    // Claude Code delivers these as an ordinary type:"user" message with a string body and no
    // isMeta flag, so nothing else filters them and extraction saw task ids and status lines as
    // things the user said. Measured at 39 across 400 local transcripts, each the whole message.
    writeFileSync(
      file,
      JSON.stringify({
        type: "user",
        message: {
          role: "user",
          content:
            "<task-notification>\n<task-id>b46ca19nr</task-id>\n" +
            "<tool-use-id>toolu_0127NeaVZbdiAsbacNvasB78</tool-use-id>\n" +
            "<status>stopped</status>\n<summary>No completion record</summary>\n</task-notification>",
        },
      })
    );

    expect(readTranscript(file)).toEqual([]); // renders empty -> no turn at all
  });

  it("keeps what the user wrote around a harness wrapper", () => {
    // The stripper removes the BLOCK, never the message: replacing the whole turn with the tag's
    // contents is what made an earlier stripper discard real user text.
    writeFileSync(
      file,
      JSON.stringify({
        type: "user",
        message: {
          role: "user",
          content: "before <task-notification><status>done</status></task-notification> after",
        },
      })
    );

    const result = readTranscript(file);
    expect(result).toHaveLength(1);
    expect(result[0]?.content).toBe("before  after");
    expect(result[0]?.content).not.toContain("status");
  });

  it("drops a <system-reminder> block if the harness ever delivers one as user text", () => {
    // Today these ride inside tool_result blocks, which this reader already drops entirely; the
    // rule is tag-structural so it holds if that placement changes.
    writeFileSync(
      file,
      JSON.stringify({
        type: "user",
        message: {
          role: "user",
          content: "<system-reminder>plan mode is active</system-reminder>\nship the fix",
        },
      })
    );

    const result = readTranscript(file);
    expect(result).toHaveLength(1);
    expect(result[0]?.content).toBe("ship the fix");
  });

  it("fails open (returns []) when the file cannot be read", () => {
    expect(readTranscript(join(root, "does-not-exist.jsonl"))).toEqual([]);
  });

  it("joins multiple text blocks in a single message with newlines", () => {
    writeFileSync(
      file,
      JSON.stringify({
        type: "assistant",
        message: {
          role: "assistant",
          content: [
            { type: "text", text: "First paragraph." },
            { type: "thinking", thinking: "irrelevant" },
            { type: "text", text: "Second paragraph." },
          ],
        },
      })
    );

    const result = readTranscript(file);

    expect(result).toEqual([{ role: "assistant", content: "First paragraph.\nSecond paragraph." }]);
  });
});

describe("transcript-util", () => {
  it("truncate caps at TOOL_TEXT_CAP with a marker, leaves shorter text alone", () => {
    expect(truncate("short")).toBe("short");
    const long = "x".repeat(TOOL_TEXT_CAP + 10);
    expect(truncate(long)).toBe(`${"x".repeat(TOOL_TEXT_CAP)}… (truncated)`);
    expect(truncate("abcdef", 4)).toBe(`${"abcd"}… (truncated)`);
  });

  it("stripInjectedMemory removes only the tagged block, keeping surrounding text", () => {
    expect(stripInjectedMemory("a <wenyuan_bank>x\ny</wenyuan_bank> b")).toBe("a  b");
    // An unterminated tag is left alone — tag-structural matching needs the closing tag.
    expect(stripInjectedMemory("a <wenyuan_bank>x")).toBe("a <wenyuan_bank>x");
  });

  it("actionLine picks the first matching target key and truncates long targets", () => {
    expect(actionLine("Edit", { file_path: "a.ts", command: "ignored" })).toBe("Edit a.ts");
    expect(actionLine("Bash", { command: "npm test" })).toBe("Bash npm test");
    // Falls back through the key list in order.
    expect(actionLine("Grep", { pattern: "foo", query: "bar" })).toBe("Grep foo");
    // A string input is the target itself (first line only).
    expect(actionLine("Custom", "line1\nline2")).toBe("Custom line1");
    // No target -> bare tool name.
    expect(actionLine("Tool", { other: 1 })).toBe("Tool");
    // Blank-ish strings don't count as targets.
    expect(actionLine("Tool", { path: "   " })).toBe("Tool");
    const long = "y".repeat(150);
    expect(actionLine("Read", { file_path: long })).toBe(`Read ${"y".repeat(100)}…`);
  });
});
