import { describe, expect, it } from "vitest";
import { contextOutput, sessionStartOutput } from "../../src/runtime/hook-output";
import {
  appendedTurns,
  conversationDocumentId,
  conversationFingerprint,
  conversationFromPayload,
  isAppendOf,
} from "../../src/runtime/conversation";
import { composeContext } from "../../src/runtime/session-start-stage";
import { composeConversation } from "../../src/runtime/stop-stage";

/**
 * The envelopes the hook stages emit, and the conversation identity and content
 * fingerprint the stop stage writes back under.
 *
 * An envelope is built only when it carries at least one member, so the stages
 * emit nothing rather than an empty object; the document identity is stable per
 * session, and the fingerprint decides append versus replace.
 */

describe("the session-start envelope", () => {
  it("carries a user message and a context block when both are present", () => {
    const output = sessionStartOutput("SessionStart", "hello", "- a");
    expect(output).toEqual({
      systemMessage: "hello",
      hookSpecificOutput: { hookEventName: "SessionStart", additionalContext: "- a" },
    });
  });

  it("emits nothing when there is neither a message nor a context block", () => {
    expect(sessionStartOutput("SessionStart", "", undefined)).toBeNull();
    expect(sessionStartOutput("SessionStart", undefined, "")).toBeNull();
  });

  it("carries just the message when there is no context block", () => {
    const output = sessionStartOutput("SessionStart", "hi", undefined);
    expect(output).toEqual({ systemMessage: "hi" });
  });
});

describe("the before-prompt envelope", () => {
  it("routes the context block under the host's own event spelling", () => {
    expect(contextOutput("UserPromptSubmit", "- a")).toEqual({
      hookSpecificOutput: { hookEventName: "UserPromptSubmit", additionalContext: "- a" },
    });
  });

  it("is null when there is no context to inject, so nothing empty is emitted", () => {
    expect(contextOutput("UserPromptSubmit", undefined)).toBeNull();
    expect(contextOutput("UserPromptSubmit", "")).toBeNull();
  });
});

describe("the conversation identity and fingerprint", () => {
  it("is stable across firings of one session and distinct across sessions", () => {
    expect(conversationDocumentId("claude-code", "s-1")).toBe(conversationDocumentId("claude-code", "s-1"));
    expect(conversationDocumentId("claude-code", "s-1")).not.toBe(conversationDocumentId("claude-code", "s-2"));
    expect(conversationDocumentId("claude-code", "s-1")).not.toBe(conversationDocumentId("codex", "s-1"));
  });

  it("changes whenever the turns change", () => {
    expect(conversationFingerprint(["a"])).not.toBe(conversationFingerprint(["a", "b"]));
    expect(conversationFingerprint(["a", "b"])).not.toBe(conversationFingerprint(["b", "a"]));
  });

  it("recognizes an appended conversation but not a rewritten one", () => {
    expect(isAppendOf(["a"], ["a", "b"])).toBe(true);
    expect(isAppendOf(["a", "b"], ["a"])).toBe(false);
    expect(isAppendOf(["a", "b"], ["a", "c"])).toBe(false);
    expect(isAppendOf(["a"], ["a"])).toBe(false);
  });

  it("yields only the newly appended turns, or the whole conversation when rewritten", () => {
    expect(appendedTurns(["a"], ["a", "b", "c"])).toEqual(["b", "c"]);
    expect(appendedTurns(["a", "b"], ["x"])).toEqual(["x"]);
  });
});

describe("reading the conversation from a payload", () => {
  it("takes the turns and the session id from the members a payload may carry", () => {
    const conversation = conversationFromPayload({ session_id: "s-1", turns: ["a", "b"] });
    expect(conversation.sessionId).toBe("s-1");
    expect(conversation.turns).toEqual(["a", "b"]);
  });

  it("reads the content of structured turn members", () => {
    const conversation = conversationFromPayload({ messages: [{ content: "a" }, { content: "b" }] });
    expect(conversation.turns).toEqual(["a", "b"]);
  });

  it("yields an empty conversation rather than failing when none is present", () => {
    const conversation = conversationFromPayload({});
    expect(conversation.sessionId).toBeUndefined();
    expect(conversation.turns).toEqual([]);
  });
});

describe("composing the write-backs", () => {
  it("renders retrieved texts as one bulleted context block", () => {
    expect(composeContext(["a", "b"])).toBe("- a\n- b");
  });

  it("writes only the appended turns when the conversation grew, the whole one when rewritten", () => {
    expect(composeConversation(["a"], ["a", "b"])).toBe("b");
    expect(composeConversation([], ["a", "b"])).toBe("a\nb");
    expect(composeConversation(["a", "b"], ["x"])).toBe("x");
  });
});