/**
 * The flat-registration hosts: Antigravity, Cursor and Copilot each take one `{command, timeout}`
 * object per hook rather than a matcher group, and each has its own wire envelope.
 */
import type { HookHarnessSpec } from "./types";
import { antigravityCwd, antigravityPrompt, copilotPrompt, cursorCwdOf, cursorPrompt } from "./prompts";
import { readAntigravityTranscript } from "../../core/transcript-antigravity";
import { readCopilotTranscript } from "../../core/transcript-copilot";
import { readCursorTranscript } from "../../core/transcript-cursor";

export const antigravityCli: HookHarnessSpec = {
  configStyle: "flat",
  install: {
    // PreInvocation is Antigravity's only lifecycle point that can inject context. Its first
    // invocation also performs the SessionStart responsibilities through runHook's seed guard.
    sessionStart: { event: "PreInvocation", entry: "pre-prompt.mjs", timeout: 30 },
    prompt: { event: "PreInvocation", entry: "pre-prompt.mjs", timeout: 30 },
    stop: { event: "Stop", entry: "stop.mjs", timeout: 30 },
  },
  sessionStart: {
    harness: "antigravity-cli",
    parse: (ev) => ({
      cwd: antigravityCwd(ev),
      sessionId: ev.conversationId as string | undefined,
    }),
    emit: () => ({}),
  },
  prompt: antigravityPrompt,
  retain: {
    hostTimeoutSec: 30,
    harness: "antigravity-cli",
    parse: (ev) => ({
      sessionId: ev.conversationId as string | undefined,
      transcriptPath: ev.transcriptPath as string | undefined,
      cwd: antigravityCwd(ev),
    }),
    readTranscript: readAntigravityTranscript,
  },
};

export const cursorCli: HookHarnessSpec = {
  configStyle: "flat",
  install: {
    sessionStart: { event: "sessionStart", entry: "session-start.mjs", timeout: 30 },
    prompt: { event: "beforeSubmitPrompt", entry: "pre-prompt.mjs" },
    stop: { event: "stop", entry: "stop.mjs", timeout: 30 },
  },
  sessionStart: {
    harness: "cursor-cli",
    parse: (ev) => ({
      cwd: cursorCwdOf(ev),
      sessionId:
        (ev.conversation_id as string | undefined) ?? (ev.session_id as string | undefined),
    }),
    emit: (out) => ({
      ...(out.additionalContext ? { additional_context: out.additionalContext } : {}),
    }),
  },
  prompt: cursorPrompt,
  retain: {
    hostTimeoutSec: 30,
    harness: "cursor-cli",
    parse: (ev) => ({
      sessionId:
        (ev.conversation_id as string | undefined) ?? (ev.session_id as string | undefined),
      transcriptPath: ev.transcript_path as string | undefined,
      cwd: cursorCwdOf(ev),
    }),
    readTranscript: readCursorTranscript,
  },
};

export const copilotCli: HookHarnessSpec = {
  configStyle: "flat",
  install: {
    sessionStart: { event: "sessionStart", entry: "session-start.mjs", timeout: 30 },
    prompt: { event: "userPromptTransformed", entry: "pre-prompt.mjs", timeout: 30 },
    stop: { event: "agentStop", entry: "stop.mjs", timeout: 60 },
  },
  sessionStart: {
    harness: "copilot-cli",
    parse: (ev) => ({
      cwd: ev.cwd as string | undefined,
      sessionId: ev.sessionId as string | undefined,
    }),
    // Copilot CLI's SessionStart response only has model-facing `additionalContext`; unlike
    // Claude/Cursor it has no supported in-TUI system-message/banner channel. Keep memory
    // quiet rather than auto-submitting a synthetic prompt or showing an OS notification. When
    // Copilot exposes a real TUI extension point, add the banner there without changing this
    // shared lifecycle output.
    emit: (out) => ({
      ...(out.additionalContext ? { additionalContext: out.additionalContext } : {}),
    }),
  },
  prompt: copilotPrompt,
  retain: {
    hostTimeoutSec: 60,
    harness: "copilot-cli",
    parse: (ev) => ({
      sessionId: ev.sessionId as string | undefined,
      transcriptPath: ev.transcriptPath as string | undefined,
      cwd: ev.cwd as string | undefined,
    }),
    readTranscript: readCopilotTranscript,
  },
};
