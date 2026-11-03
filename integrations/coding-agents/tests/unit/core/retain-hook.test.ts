import { mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { deriveBankId } from "../../../src/core/bank";
import type { MemoryClient } from "../../../src/core/client";
import { resolveConfig, type RawConfig } from "../../../src/core/config";
import { buildRetain, runRetainHook } from "../../../src/core/retain-hook";
import {
  fingerprintTurns,
  memoryCursorStore,
  type RetainCursorStore,
} from "../../../src/core/retain-cursor";
import type { TransportTurn } from "../../../src/core/transcript";
import { readCodexTranscript } from "../../../src/core/transcript-codex";
import { dcodeAssistantText } from "../../../src/core/transcript-dcode";
import { memoryUsageCursorStore } from "../../../src/core/usage";

/** The Stop event `runRetainHook` reads from fd 0; every other read stays real. */
let stdin = "";
vi.mock("node:fs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("node:fs")>();
  return {
    ...actual,
    readFileSync: (target: unknown, ...rest: unknown[]) =>
      target === 0 ? stdin : (actual.readFileSync as (...a: unknown[]) => unknown)(target, ...rest),
  };
});

/** What the hook's `loadConfig` returns — the real resolver over a per-test raw config, so bank
 *  overrides and defaults behave exactly as they do against a real config file. */
let rawConfig: RawConfig = {};
vi.mock("../../../src/core/config", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../../src/core/config")>();
  return { ...actual, loadConfig: () => actual.resolveConfig(rawConfig) };
});

let root: string;
let file: string;

beforeEach(() => {
  root = mkdtempSync(join(tmpdir(), "wy-retain-hook-"));
  file = join(root, "session.jsonl");
});

afterEach(() => {
  rmSync(root, { recursive: true, force: true });
});

/** One Codex rollout `response_item` message line. */
const message = (role: string, text: string, phase?: string) =>
  JSON.stringify({
    type: "response_item",
    payload: {
      type: "message", role, phase,
      content: [{ type: role === "user" ? "input_text" : "output_text", text }],
    },
  });

/** The `item_completed` event that marks a user line as a genuine prompt, not injected text. */
const userEvent = (text: string) =>
  JSON.stringify({
    type: "event_msg",
    payload: { type: "item_completed", item: { type: "UserMessage", content: [{ type: "text", text }] } },
  });

/** A retain call as a positional array (indexed access needs the narrowing). */
const callAt = (retain: ReturnType<typeof vi.fn>, n: number): unknown[] =>
  (retain.mock.calls[n] ?? []) as unknown[];

/** Common codex buildRetain args: the real reader, in-memory cursor, append-capable client. */
const codexArgs = (retain: ReturnType<typeof vi.fn>, sessionId: string, over: object = {}) =>
  ({
    harness: "codex",
    sessionId,
    readTranscript: readCodexTranscript,
    cursors: memoryCursorStore(),
    client: { retain, bank: "test-bank", supportsAppendRetain: async () => true },
    ...over,
  }) as unknown as Parameters<typeof buildRetain>[0];

describe("buildRetain usage stats", () => {
  it("records the tool calls and credit of a real Claude Code transcript", async () => {
    // The raw host format end to end: tool_use blocks through the reader's action turns.
    const usageFile = join(root, "usage.jsonl");
    vi.stubEnv("WENYUAN_AGENTS_USAGE_FILE", usageFile);
    const msg = (type: string, content: unknown) =>
      JSON.stringify({ type, message: { role: type, content } });
    writeFileSync(
      file,
      [
        msg("user", "how do we round?"),
        msg("assistant", [
          { type: "tool_use", name: "mcp__wenyuan__wenyuan_search_knowledge_pages", input: { query: "rounding" } },
          { type: "tool_use", name: "Grep", input: { pattern: "round" } },
        ]),
        msg("user", [{ type: "tool_result", content: "page kp-1" }]),
        msg("assistant", [{ type: "text", text: "> 🧠 **From WenYuan memory** — half up" }]),
      ].join("\n")
    );
    try {
      await buildRetain({
        harness: "claude-code",
        sessionId: "sess-usage",
        transcriptPath: file,
        bankId: "bank-1",
        usageCursors: memoryUsageCursorStore(),
        client: { retain: vi.fn().mockResolvedValue(undefined) } as unknown as MemoryClient,
      });
      expect(JSON.parse(readFileSync(usageFile, "utf8"))).toMatchObject({
        harness: "claude-code",
        session: "sess-usage",
        bank: "bank-1",
        turn: 1,
        calls: ["wenyuan_search_knowledge_pages"],
        credited: true,
      });
    } finally {
      vi.unstubAllEnvs();
    }
  });
});

describe("buildRetain", () => {
  it("retains only UserMessage-event user turns and advances its cursor normally", async () => {
    const genuine = "# AGENTS.md instructions for /example\nExplain this heading.";
    const lines = [
      message("user", "<recommended_plugins>guidance</recommended_plugins>"),
      message("user", genuine),
      userEvent(genuine),
      message("assistant", "I will check.", "commentary"),
      JSON.stringify({
        type: "response_item",
        payload: { type: "function_call", name: "calculator", arguments: "{}" },
      }),
      JSON.stringify({ type: "response_item", payload: { type: "function_call_output", output: "raw output" } }),
      message("assistant", "Done.", "final_answer"),
    ];
    const retain = vi.fn().mockResolvedValue(undefined);
    const args = codexArgs(retain, "sess-origin", { transcriptPath: file });
    writeFileSync(file, lines.join("\n"));
    await buildRetain(args);
    expect(retain).toHaveBeenCalledTimes(1);
    expect(callAt(retain, 0)[2]).toBe("conversation:sess-origin");
    expect((callAt(retain, 0)[5] as { updateMode?: string }).updateMode).toBeUndefined();
    expect(String(callAt(retain, 0)[0]).split("\n").map((l) => JSON.parse(l))).toEqual([
      { role: "system", content: "REF-ID: conversation:sess-origin", timestamp: expect.any(String) },
      { role: "user", content: genuine },
      { role: "assistant", content: "I will check." },
      { role: "action", content: "calculator" },
      { role: "assistant", content: "Done." },
    ]);
    await buildRetain(args);
    expect(retain).toHaveBeenCalledTimes(1);
    lines.push(message("user", "Next question"), userEvent("Next question"), message("assistant", "Next answer"));
    writeFileSync(file, lines.join("\n"));
    await buildRetain(args);
    expect(retain).toHaveBeenCalledTimes(2);
    expect((callAt(retain, 1)[5] as { updateMode?: string }).updateMode).toBe("append");
    expect(String(callAt(retain, 1)[0]).split("\n").map((l) => JSON.parse(l))).toEqual([
      { role: "user", content: "Next question" },
      { role: "assistant", content: "Next answer" },
    ]);
  });

  it.each([false, true])(
    "does not retain or clean up empty filtered turns (existing cursor: %s)",
    async (existing) => {
      writeFileSync(
        file,
        JSON.stringify({
          type: "response_item",
          payload: {
            type: "message",
            role: "user",
            content: [{ type: "input_text", text: "<environment_context>synthetic</environment_context>" }],
            internal_chat_message_metadata_passthrough: {
              content_item_kinds: ["environments.environment_context"],
            },
          },
        })
      );
      const retain = vi.fn().mockResolvedValue(undefined);
      const cursors = memoryCursorStore();
      if (existing)
        cursors.write("sess-empty", {
          turns: 1,
          fingerprint: fingerprintTurns(
            [{ role: "user", content: "<environment_context>synthetic</environment_context>" }],
            1
          ),
          bank: "test-bank",
        });
      const previous = cursors.read("sess-empty");
      const args = codexArgs(retain, "sess-empty", { transcriptPath: file, cursors });
      await buildRetain(args);
      await buildRetain(args);
      expect(retain).not.toHaveBeenCalled();
      expect(cursors.read("sess-empty")).toEqual(previous);
    }
  );

  it("filters fallback provenance and internal channels through repeated Stop retention", async () => {
    const stamp = "2026-09-22T12:00:00Z";
    const msgLine = (role: string, content: unknown[], extra = {}) =>
      JSON.stringify({
        type: "response_item",
        timestamp: stamp,
        payload: { type: "message", role, content, ...extra },
      });
    const lines = [
      msgLine(
        "user",
        [
          { type: "input_text", text: "<environment_context>synthetic</environment_context>" },
          { type: "input_image", image_url: "synthetic" },
          { type: "input_text", text: "Keep ID TASK-42" },
        ],
        {
          internal_chat_message_metadata_passthrough: {
            content_item_kinds: ["environments.environment_context", "user.image", "user.text"],
          },
        }
      ),
      msgLine("assistant", [{ text: "private reasoning" }], { channel: "analysis" }),
      msgLine("assistant", [{ text: "Saved TASK-42" }], { phase: "final_answer" }),
    ];
    const retain = vi.fn().mockResolvedValue(undefined);
    const args = codexArgs(retain, "sess-filter", { transcriptPath: file });
    writeFileSync(file, lines.join("\n"));
    await buildRetain(args);
    expect(callAt(retain, 0)[2]).toBe("conversation:sess-filter");
    expect(
      String(callAt(retain, 0)[0]).split("\n").map((r) => JSON.parse(r)).slice(1)
    ).toEqual([
      { role: "user", content: "Keep ID TASK-42", timestamp: stamp },
      { role: "assistant", content: "Saved TASK-42", timestamp: stamp },
    ]);
    await buildRetain(args);
    lines.push(msgLine("assistant", [{ text: "more internal text" }], { phase: "reasoning" }));
    writeFileSync(file, lines.join("\n"));
    await buildRetain(args);
    expect(retain).toHaveBeenCalledTimes(1);
    lines.push(msgLine("user", [{ text: "Next prompt" }]), msgLine("assistant", [{ text: "Next answer" }]));
    writeFileSync(file, lines.join("\n"));
    await buildRetain(args);
    expect(retain).toHaveBeenCalledTimes(2);
    expect(callAt(retain, 1)[2]).toBe("conversation:sess-filter");
    expect((callAt(retain, 1)[5] as { updateMode?: string }).updateMode).toBe("append");
    expect(String(callAt(retain, 1)[0]).split("\n").map((r) => JSON.parse(r))).toEqual([
      { role: "user", content: "Next prompt", timestamp: stamp },
      { role: "assistant", content: "Next answer", timestamp: stamp },
    ]);
  });

  it("appends a Codex continuation rollout instead of replacing the session document", async () => {
    // Same session_meta id, a NEW rollout file holding only the turns that come next: the
    // earlier turns live in the earlier file, so replacing would drop them from the document.
    const segment = (user: string, reply: string) =>
      [message("user", user), userEvent(user), message("assistant", reply, "final_answer")].join("\n");

    const retain = vi.fn().mockResolvedValue(undefined);
    const args = codexArgs(retain, "same-session");
    const fileB = join(root, "rollout-b.jsonl");
    writeFileSync(file, segment("The Atlas connector is amber.", "Noted."));
    await buildRetain({ ...args, transcriptPath: file });
    writeFileSync(fileB, segment("The Birch connector is violet.", "Noted too."));
    await buildRetain({ ...args, transcriptPath: fileB });

    expect(retain).toHaveBeenCalledTimes(2);
    expect((callAt(retain, 1)[5] as { updateMode?: string }).updateMode).toBe("append");
    expect(String(callAt(retain, 1)[0]).split("\n").map((l) => JSON.parse(l))).toEqual([
      { role: "user", content: "The Birch connector is violet." },
      { role: "assistant", content: "Noted too." },
    ]);
    // Retrying the same continuation adds nothing; its own next turns append from where it left off.
    await buildRetain({ ...args, transcriptPath: fileB });
    expect(retain).toHaveBeenCalledTimes(2);
    writeFileSync(
      fileB,
      [segment("The Birch connector is violet.", "Noted too."), segment("And?", "That is all.")].join("\n")
    );
    await buildRetain({ ...args, transcriptPath: fileB });
    expect(retain).toHaveBeenCalledTimes(3);
    expect((callAt(retain, 2)[5] as { updateMode?: string }).updateMode).toBe("append");
    expect(String(callAt(retain, 2)[0]).split("\n").map((l) => JSON.parse(l))).toEqual([
      { role: "user", content: "And?" },
      { role: "assistant", content: "That is all." },
    ]);
  });

  it("does not re-append an earlier rollout whose hook lands after the session moved on", async () => {
    // A -> B -> A -> B, the shape a delayed Stop produces. A is already in the document, so its
    // late hook replaces rather than appending a second time; the replace forgets B, so B's next
    // hook appends B back instead of replacing in turn. Bounded either way; appending on every
    // alternation was not.
    const segment = (user: string, reply: string) =>
      [message("user", user), userEvent(user), message("assistant", reply, "final_answer")].join("\n");
    const retain = vi.fn().mockResolvedValue(undefined);
    const args = codexArgs(retain, "late-hook-session");
    const fileB = join(root, "rollout-late-b.jsonl");
    writeFileSync(file, segment("The Atlas connector is amber.", "Noted."));
    writeFileSync(fileB, segment("The Birch connector is violet.", "Noted too."));

    await buildRetain({ ...args, transcriptPath: file });
    await buildRetain({ ...args, transcriptPath: fileB });
    await buildRetain({ ...args, transcriptPath: file });
    await buildRetain({ ...args, transcriptPath: fileB });

    const modes = retain.mock.calls.map(
      (c) => ((c as unknown[])[5] as { updateMode?: string }).updateMode
    );
    expect(modes).toEqual([
      undefined, // A: first write, a replace
      "append", // B: a continuation segment
      undefined, // A again: already written, so a replace — NOT its turns a second time
      "append", // B again: the replace forgot it, so it comes back as a segment
    ]);
    // Every append carried one segment's turns, never a whole file replayed on top of itself.
    modes.forEach((m, i) => {
      if (m === "append") expect(String(callAt(retain, i)[0]).split("\n")).toHaveLength(2);
    });
  });

  it("removes startup boilerplate from the document, preserves conversation, then appends", async () => {
    const startup =
      "<recommended_plugins>Use available tools.</recommended_plugins>\n" +
      "# AGENTS.md instructions\n<INSTRUCTIONS>Follow project conventions.</INSTRUCTIONS>\n" +
      "<environment_context><cwd>/example</cwd></environment_context>";
    const lines = [
      message("user", startup),
      message("user", "What is 2 + 2?"),
      userEvent("What is 2 + 2?"),
      message("assistant", "I will calculate it.", "commentary"),
      JSON.stringify({
        type: "response_item",
        payload: { type: "function_call", name: "calculator", arguments: "{}" },
      }),
      message("assistant", "4.", "final_answer"),
    ];
    const conversation: TransportTurn[] = [
      { role: "user", content: "What is 2 + 2?" },
      { role: "assistant", content: "I will calculate it." },
      { role: "action", content: "calculator" },
      { role: "assistant", content: "4." },
    ];
    const previouslyRetained: TransportTurn[] = [{ role: "user", content: startup }, ...conversation];
    const cursors = memoryCursorStore();
    // A normal Stop after upgrading encounters the old prefix; no historical replay is needed.
    cursors.write("sess-desktop", {
      turns: previouslyRetained.length,
      fingerprint: fingerprintTurns(previouslyRetained, previouslyRetained.length),
      bank: "test-bank",
    });
    const retain = vi.fn().mockResolvedValue(undefined);
    const args = codexArgs(retain, "sess-desktop", { transcriptPath: file, cursors });
    writeFileSync(file, lines.join("\n"));
    await buildRetain(args);
    expect(retain).toHaveBeenCalledTimes(1);
    const [content, , documentId, tags, strategy, options] = callAt(retain, 0);
    expect(documentId).toBe("conversation:sess-desktop");
    expect(tags).toEqual(["source:chat", "harness:codex"]);
    expect(strategy).toBe("conversation");
    expect((options as { updateMode?: string }).updateMode).toBeUndefined();
    expect(String(content).split("\n").map((l) => JSON.parse(l))).toEqual([
      { role: "system", content: "REF-ID: conversation:sess-desktop", timestamp: expect.any(String) },
      ...conversation,
    ]);

    lines.push(message("user", "And 3 + 3?"), userEvent("And 3 + 3?"), message("assistant", "6."));
    writeFileSync(file, lines.join("\n"));
    await buildRetain(args);
    expect(retain).toHaveBeenCalledTimes(2);
    expect((callAt(retain, 1)[5] as { updateMode?: string }).updateMode).toBe("append");
    expect(String(callAt(retain, 1)[0]).split("\n").map((l) => JSON.parse(l))).toEqual([
      { role: "user", content: "And 3 + 3?" },
      { role: "assistant", content: "6." },
    ]);
    await buildRetain(args);
    expect(retain).toHaveBeenCalledTimes(2);
  });

  /** Turns retained by one buildRetain call, in order. */
  async function retainedTurns(
    args: Parameters<typeof buildRetain>[0] & { retainSpy: ReturnType<typeof vi.fn> }
  ): Promise<Array<{ role: string; content: string }>> {
    const { retainSpy, ...rest } = args;
    await buildRetain({ ...rest, client: { retain: retainSpy } as unknown as MemoryClient });
    const [content] = callAt(retainSpy, 0);
    return String(content).split("\n").map((line) => JSON.parse(line));
  }

  /** The dcode lag fixture: a transcript holding only the user turn, Stop event alongside. */
  async function dcodeRun(over: Record<string, unknown>) {
    writeFileSync(file, JSON.stringify({ role: "user", content: "make the change" }));
    return retainedTurns({
      harness: "dcode",
      sessionId: "sess-dcode",
      transcriptPath: file,
      client: undefined as never,
      retainSpy: vi.fn().mockResolvedValue(undefined),
      readTranscript: () => [{ role: "user", content: "make the change" }],
      ...over,
    } as Parameters<typeof buildRetain>[0] & { retainSpy: ReturnType<typeof vi.fn> });
  }

  it("recovers the final assistant message when the materialized transcript lags", async () => {
    const parsed = await dcodeRun({
      lastAssistantMessage: "done <wenyuan_memories>injected</wenyuan_memories>",
    });
    expect(parsed.at(-1)).toMatchObject({ role: "assistant", content: "done" });
  });

  it("does not duplicate the final reply when the transcript already flushed it", async () => {
    // The branch the lag recovery has to not break: on runs where the store flushed in time the
    // reply is in BOTH the transcript and the Stop event, and must be retained exactly once.
    const parsed = await dcodeRun({
      readTranscript: () => [
        { role: "user", content: "make the change" },
        { role: "assistant", content: "done" },
      ],
      lastAssistantMessage: "done",
    });
    expect(parsed.filter((t) => t.role === "assistant")).toHaveLength(1);
  });

  it("dedupes a flushed reply the harness serialized as content blocks", async () => {
    // The host sends str(content), so without readLastMessage the Stop event's copy can never
    // compare equal to the transcript's clean text and a duplicate is appended every turn.
    const parsed = await dcodeRun({
      readTranscript: () => [
        { role: "user", content: "make the change" },
        { role: "assistant", content: "done" },
      ],
      lastAssistantMessage: "[{'type': 'text', 'text': 'done'}]",
      readLastMessage: dcodeAssistantText,
    });
    expect(parsed.filter((t) => t.role === "assistant")).toHaveLength(1);
  });

  it("retains the recovered reply as text, not as a serialized block list", async () => {
    const parsed = await dcodeRun({
      lastAssistantMessage:
        "[{'type': 'reasoning', 'encrypted_content': 'gAAAAAsecret'}, " +
        "{'type': 'text', 'text': 'done'}]",
      readLastMessage: dcodeAssistantText,
    });
    expect(parsed.at(-1)).toMatchObject({ role: "assistant", content: "done" });
  });

  it("retains parsed turns", async () => {
    writeFileSync(
      file,
      [
        '{"type":"user","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"we use zod for validation"}}',
        '{"type":"assistant","timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"noted, zod it is"}]}}',
      ].join("\n")
    );

    const retainSpy = vi.fn().mockResolvedValue(undefined);
    const client = { retain: retainSpy } as unknown as MemoryClient;

    await buildRetain({
      harness: "claude-code",
      sessionId: "sess-1",
      transcriptPath: file,
      client,
    });

    expect(retainSpy).toHaveBeenCalledTimes(1);
    const [content, , documentId, tags, strategy] = callAt(retainSpy, 0);
    expect(documentId).toBe("conversation:sess-1");
    // A JSONL transcript (renderSessionJsonl): one {role, content, timestamp} object per line,
    // led by the REF-ID system turn.
    const parsed = String(content)
      .split("\n")
      .map((line) => JSON.parse(line) as { role: string; content: string });
    expect(parsed[0]).toMatchObject({ role: "system", content: "REF-ID: conversation:sess-1" });
    expect(parsed[1]).toMatchObject({ role: "user", content: "we use zod for validation" });
    expect(parsed[2]).toMatchObject({ role: "assistant", content: "noted, zod it is" });
    // Verbose `session` extraction, not the ≤2-fact `chat` extractor.
    expect(strategy).toBe("conversation");
    expect(tags).toEqual(["source:chat", "harness:claude-code"]);
  });

  it("empty transcript -> no retain", async () => {
    const lines = [
      // isMeta line: dropped
      '{"type":"user","isMeta":true,"message":{"role":"user","content":"<system-injected>"}}',
      // non-message summary line: dropped
      '{"type":"summary","summary":"…"}',
    ];
    writeFileSync(file, lines.join("\n"));

    const retainSpy = vi.fn().mockResolvedValue(undefined);
    const client = { retain: retainSpy } as unknown as MemoryClient;

    await buildRetain({
      harness: "claude-code",
      sessionId: "sess-2",
      transcriptPath: file,
      client,
    });

    expect(retainSpy).not.toHaveBeenCalled();
  });

  it("fails open on retain error", async () => {
    writeFileSync(
      file,
      '{"type":"user","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"hello"}}'
    );

    const retainSpy = vi.fn().mockRejectedValue(new Error("boom"));
    const client = { retain: retainSpy } as unknown as MemoryClient;

    await expect(
      buildRetain({
        harness: "claude-code",
        sessionId: "sess-3",
        transcriptPath: file,
        client,
      })
    ).resolves.toBeUndefined();
  });
});

describe("runRetainHook anti-recursion guard", () => {
  const ORIGINAL = process.env.WENYUAN_DISABLE_HOOKS;

  afterEach(() => {
    if (ORIGINAL === undefined) delete process.env.WENYUAN_DISABLE_HOOKS;
    else process.env.WENYUAN_DISABLE_HOOKS = ORIGINAL;
  });

  it("WENYUAN_DISABLE_HOOKS set -> returns immediately, never reads stdin or builds a client", async () => {
    process.env.WENYUAN_DISABLE_HOOKS = "1";
    const makeClient = vi.fn();
    // No stdin is provided/mocked here — if the guard didn't return before reading fd 0, this
    // call would read the real process stdin. Resolving without makeClient proves the guard fired.
    await runRetainHook(
      { harness: "claude-code", hostTimeoutSec: 60, parse: () => ({}) },
      makeClient as never
    );
    expect(makeClient).not.toHaveBeenCalled();
  });
});

describe("buildRetain — incremental write-back across Stop hooks", () => {
  const line = (i: number) =>
    JSON.stringify({ type: "user", timestamp: `2026-01-01T00:00:0${i}Z`, message: { role: "user", content: `turn ${i}` } });

  /** One Stop-hook invocation over the transcript as it stands; only the cursor store carries
   *  state between calls (each hook run is a fresh process in production). */
  const stop = async (client: MemoryClient, cursors: RetainCursorStore) =>
    buildRetain({ harness: "codex", sessionId: "sess-append", transcriptPath: file, client, cursors });

  const stubClient = () => {
    const retain = vi.fn().mockResolvedValue(undefined);
    return {
      retain,
      client: { retain, bank: "coding-agent::repo", supportsAppendRetain: async () => true } as unknown as MemoryClient,
    };
  };

  it("sends the whole session once, then only the turns the session grew by", async () => {
    const { retain, client } = stubClient();
    const cursors = memoryCursorStore();

    writeFileSync(file, [line(0), line(1)].join("\n"));
    await stop(client, cursors);

    writeFileSync(file, [line(0), line(1), line(2)].join("\n"));
    await stop(client, cursors);

    expect(retain).toHaveBeenCalledTimes(2);
    // First write: REF-ID header + both turns; second: turn 2 alone.
    expect(String(callAt(retain, 0)[0]).split("\n")).toHaveLength(3);
    expect((callAt(retain, 0)[5] as { updateMode?: string }).updateMode).toBeUndefined();
    const appended = String(callAt(retain, 1)[0]).split("\n");
    expect(appended).toHaveLength(1);
    expect(JSON.parse(appended[0] ?? "")).toMatchObject({ role: "user", content: "turn 2" });
    expect((callAt(retain, 1)[5] as { updateMode?: string }).updateMode).toBe("append");
  });

  it("does not write again when the session ended without new turns", async () => {
    const { retain, client } = stubClient();
    const cursors = memoryCursorStore();
    writeFileSync(file, [line(0), line(1)].join("\n"));
    await stop(client, cursors);
    await stop(client, cursors);
    expect(retain).toHaveBeenCalledTimes(1);
  });

  it("replaces the document when the transcript was rewritten rather than extended", async () => {
    const { retain, client } = stubClient();
    const cursors = memoryCursorStore();
    writeFileSync(file, [line(0), line(1), line(2)].join("\n"));
    await stop(client, cursors);

    // Compaction: earlier turns replaced by a summary, then the session continues.
    writeFileSync(
      file,
      [
        JSON.stringify({
          type: "user",
          timestamp: "2026-01-01T00:00:09Z",
          message: { role: "user", content: "summary of the work so far" },
        }),
        line(3),
      ].join("\n")
    );
    await stop(client, cursors);

    expect((callAt(retain, 1)[5] as { updateMode?: string }).updateMode).toBeUndefined();
    const rewritten = String(callAt(retain, 1)[0]).split("\n");
    expect(rewritten).toHaveLength(3); // REF-ID + the two turns that now exist
    expect(JSON.parse(rewritten[1] ?? "")).toMatchObject({ content: "summary of the work so far" });
  });

  it("a failed write is not silently skipped by the next one — it is replayed", async () => {
    const { retain, client } = stubClient();
    const cursors = memoryCursorStore();
    writeFileSync(file, [line(0), line(1)].join("\n"));
    await stop(client, cursors);

    retain.mockRejectedValueOnce(new Error("server unreachable"));
    writeFileSync(file, [line(0), line(1), line(2)].join("\n"));
    await stop(client, cursors); // buildRetain swallows the failure by design
    const failed = callAt(retain, 1);

    writeFileSync(file, [line(0), line(1), line(2), line(3)].join("\n"));
    await stop(client, cursors);

    // The next Stop replays the failed slice verbatim, then appends the new one. Neither is a
    // replace: an outage costs one retried append, not a re-extraction per Stop.
    expect(retain).toHaveBeenCalledTimes(4);
    expect(callAt(retain, 2)[0]).toBe(failed[0]);
    expect((callAt(retain, 2)[5] as { operationId?: string }).operationId).toBe(
      (failed[5] as { operationId?: string }).operationId
    );
    expect(
      retain.mock.calls
        .slice(1)
        .map((c) => ((c as unknown[])[5] as { updateMode?: string }).updateMode)
    ).toEqual(["append", "append", "append"]);
  });
});

/**
 * The `retainSessions: false` opt-out. These drive the real `runRetainHook` — stdin event in,
 * config resolved through the real loader — because the past incident was precisely a missing
 * wire between the two, which a test of either half alone cannot see.
 */
describe("runRetainHook honors retainSessions", () => {
  const event = () => ({
    // Fresh session id per test: `sessionRootDir` caches a session's starting directory, so a
    // reused id would pin every test to the wrong bank.
    session_id: `sess-gate-${basename(root)}`,
    transcript_path: file,
    cwd: root,
  });

  const spec = {
    harness: "claude-code",
    hostTimeoutSec: 60,
    parse: (ev: Record<string, unknown>) => ({
      sessionId: ev.session_id as string,
      transcriptPath: ev.transcript_path as string,
      cwd: ev.cwd as string,
    }),
  };

  beforeEach(() => {
    vi.stubEnv("WENYUAN_AGENTS_DIAG_FILE", join(root, "diag.log"));
    rawConfig = {};
    writeFileSync(
      file,
      '{"type":"user","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"we use zod for validation"}}'
    );
    stdin = JSON.stringify(event());
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  const stubClient = () => {
    const retain = vi.fn().mockResolvedValue(undefined);
    const makeClient = vi.fn(() => ({
      retain,
      supportsAppendRetain: async () => false,
    })) as unknown as Parameters<typeof runRetainHook>[1];
    return { retain, makeClient };
  };

  it("writes the transcript back by default", async () => {
    const { retain, makeClient } = stubClient();
    await runRetainHook(spec, makeClient);
    expect(retain).toHaveBeenCalledTimes(1);
  });

  it("applies an event gate before building a client", async () => {
    const { retain, makeClient } = stubClient();
    const notificationSpec = {
      ...spec,
      accept: (ev: Record<string, unknown>) => ev.notification_type === "idle_prompt",
    };

    stdin = JSON.stringify({ ...event(), notification_type: "permission_prompt" });
    await runRetainHook(notificationSpec, makeClient);
    expect(retain).not.toHaveBeenCalled();
    expect(makeClient).not.toHaveBeenCalled();

    stdin = JSON.stringify({ ...event(), notification_type: "idle_prompt" });
    await runRetainHook(notificationSpec, makeClient);
    expect(retain).toHaveBeenCalledTimes(1);
  });

  it("retainSessions: false -> no write-back, and no client is even built", async () => {
    rawConfig = { retainSessions: false };
    const { retain, makeClient } = stubClient();
    await runRetainHook(spec, makeClient);
    expect(retain).not.toHaveBeenCalled();
    expect(makeClient).not.toHaveBeenCalled();
  });

  it("retainSessions: false still records usage, which is local only", async () => {
    const usageFile = join(root, "usage.jsonl");
    vi.stubEnv("WENYUAN_AGENTS_USAGE_FILE", usageFile);
    rawConfig = { retainSessions: false };
    const { retain, makeClient } = stubClient();
    await runRetainHook(spec, makeClient);
    expect(retain).not.toHaveBeenCalled();
    expect(JSON.parse(readFileSync(usageFile, "utf8"))).toMatchObject({
      harness: "claude-code",
      session: event().session_id,
    });
  });

  it("a banks.<id> override opts one repo out while the global default still writes", async () => {
    const bankId = deriveBankId(resolveConfig(), root, spec.harness, root);
    rawConfig = { banks: { [bankId]: { retainSessions: false } } };
    const optedOut = stubClient();
    await runRetainHook(spec, optedOut.makeClient);
    expect(optedOut.retain).not.toHaveBeenCalled();

    rawConfig = { banks: { "some-other-bank": { retainSessions: false } } };
    const untouched = stubClient();
    await runRetainHook(spec, untouched.makeClient);
    expect(untouched.retain).toHaveBeenCalledTimes(1);
  });

  it("a banks.<id> override turns write-back back on under a global opt-out", async () => {
    const bankId = deriveBankId(resolveConfig(), root, spec.harness, root);
    rawConfig = { retainSessions: false, banks: { [bankId]: { retainSessions: true } } };
    const { retain, makeClient } = stubClient();
    await runRetainHook(spec, makeClient);
    expect(retain).toHaveBeenCalledTimes(1);
  });
});

/**
 * Family-wide guard: a past incident was not a broken line but a MISSING one — the hook path
 * silently ignored `retainSessions`. Assert over the whole family: every module that puts a
 * conversation in the bank must consult the flag, whichever path it serves.
 */
describe("every session write-back path honors retainSessions", () => {
  const SRC = fileURLToPath(new URL("../../..", import.meta.url));
  const WRITERS = ["retainLiveSession(", "ingestChats("];
  /** The modules that DEFINE the writers — the callers are what must gate. */
  const DEFINITIONS = ["src/core/chat.ts"];

  function sourceFiles(dir: string, prefix = ""): string[] {
    return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
      const rel = prefix ? `${prefix}/${entry.name}` : entry.name;
      if (entry.isDirectory())
        return entry.name === "e2e" ||
          entry.name === "node_modules" ||
          entry.name === "dist"
          ? []
          : sourceFiles(join(dir, entry.name), rel);
      return entry.name.endsWith(".ts") && !entry.name.includes(".test.") ? [rel] : [];
    });
  }

  it("has no conversation-writing module that ignores the flag", () => {
    const ungated = sourceFiles(SRC).filter((rel) => {
      if (DEFINITIONS.includes(rel)) return false;
      const src = readFileSync(join(SRC, rel), "utf8");
      if (!WRITERS.some((w) => src.includes(w))) return false;
      // `writeBackEnabled` is RuntimeCore's own reading of the same flag, shared by every
      // persistent-plugin host.
      return !src.includes("cfg.retainSessions") && !src.includes("writeBackEnabled");
    });
    expect(ungated).toEqual([]);
  });
});
