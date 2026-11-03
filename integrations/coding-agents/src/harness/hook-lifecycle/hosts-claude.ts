/**
 * The hook hosts that speak the Claude Code hook protocol with only naming-level variations, plus
 * the entries whose budgets and registrations differ (grok-build's tool-hook relay, qwen-code's
 * millisecond budgets, factory-droid's Notification reuse).
 */
import type { HookHarnessSpec } from "./types";
import {
  claudePrompt,
  codexPrompt,
  devinCwd,
  devinPrompt,
  dcodePrompt,
  droidPrompt,
  qwenPrompt,
} from "./prompts";
import { notGrokHosted, standardSessionStart } from "./session-starts";
import { grokTranscriptPath, readGrokTranscript } from "../../core/transcript-grok";
import { readCodexTranscript } from "../../core/transcript-codex";
import { dcodeAssistantText, readDcodeTranscript } from "../../core/transcript-dcode";
import { readDevinTranscript } from "../../core/transcript-devin";
import { readDroidTranscript } from "../../core/transcript-droid";
import { readQwenTranscript } from "../../core/transcript-qwen";

export const claudeCode: HookHarnessSpec = {
  configStyle: "nested",
  install: {
    sessionStart: { event: "SessionStart", entry: "session-start.mjs", timeout: 30 },
    prompt: { event: "UserPromptSubmit", entry: "pre-prompt.mjs", timeout: 30 },
    stop: { event: "Stop", entry: "stop.mjs", timeout: 60 },
  },
  sessionStart: { ...standardSessionStart("claude-code"), accept: notGrokHosted },
  prompt: { ...claudePrompt, accept: notGrokHosted },
  retain: {
    hostTimeoutSec: 60,
    harness: "claude-code",
    accept: notGrokHosted,
    parse: (ev) => ({
      sessionId: ev.session_id as string | undefined,
      transcriptPath: ev.transcript_path as string | undefined,
      cwd: ev.cwd as string | undefined,
    }),
  },
};

export const codex: HookHarnessSpec = {
  configStyle: "nested",
  install: {
    sessionStart: { event: "SessionStart", entry: "session-start.mjs", timeout: 30 },
    prompt: { event: "UserPromptSubmit", entry: "pre-prompt.mjs", timeout: 30 },
    stop: { event: "Stop", entry: "stop.mjs", timeout: 60 },
  },
  sessionStart: standardSessionStart("codex"),
  prompt: codexPrompt,
  retain: {
    hostTimeoutSec: 60,
    harness: "codex",
    parse: (ev) => ({
      sessionId: ev.session_id as string | undefined,
      transcriptPath: ev.transcript_path as string | undefined,
      cwd: ev.cwd as string | undefined,
    }),
    readTranscript: readCodexTranscript,
  },
};

export const dcode: HookHarnessSpec = {
  configStyle: "nested",
  install: {
    sessionStart: { event: "SessionStart", entry: "session-start.mjs", timeout: 30 },
    prompt: { event: "UserPromptSubmit", entry: "pre-prompt.mjs", timeout: 30 },
    stop: { event: "Stop", entry: "stop.mjs", timeout: 60 },
  },
  sessionStart: standardSessionStart("dcode"),
  prompt: dcodePrompt,
  retain: {
    hostTimeoutSec: 60,
    harness: "dcode",
    parse: (ev) => ({
      sessionId: ev.session_id as string | undefined,
      transcriptPath: ev.transcript_path as string | undefined,
      cwd: ev.cwd as string | undefined,
      lastAssistantMessage: ev.last_assistant_message as string | undefined,
    }),
    readTranscript: readDcodeTranscript,
    readLastMessage: dcodeAssistantText,
  },
};

export const devinCli: HookHarnessSpec = {
  configStyle: "nested",
  install: {
    sessionStart: { event: "SessionStart", entry: "session-start.mjs", timeout: 30 },
    prompt: { event: "UserPromptSubmit", entry: "pre-prompt.mjs", timeout: 30 },
    stop: { event: "Stop", entry: "stop.mjs", timeout: 60 },
  },
  sessionStart: {
    harness: "devin-cli",
    parse: (ev) => ({ cwd: devinCwd(), sessionId: ev.session_id as string | undefined }),
    emit: (out) => ({
      hookSpecificOutput: {
        hookEventName: "SessionStart",
        ...(out.additionalContext ? { additionalContext: out.additionalContext } : {}),
      },
    }),
  },
  prompt: devinPrompt,
  retain: {
    hostTimeoutSec: 60,
    harness: "devin-cli",
    parse: (ev) => {
      const sessionId = ev.session_id as string | undefined;
      return {
        sessionId,
        // RetainHook calls the supplied reader with transcriptPath; Devin's reader uses its
        // session id because the CLI persists conversations in sessions.db rather than a file.
        transcriptPath: sessionId,
        cwd: devinCwd(),
      };
    },
    readTranscript: readDevinTranscript,
  },
};

export const grokBuild: HookHarnessSpec = {
  configStyle: "nested",
  install: {
    sessionStart: { event: "SessionStart", entry: "session-start.mjs", timeout: 30 },
    prompt: { event: "UserPromptSubmit", entry: "pre-prompt.mjs", timeout: 30 },
    stop: { event: "Stop", entry: "stop.mjs", timeout: 60 },
  },
  // Grok hands the model `additionalContext` from TOOL hooks only: SessionStart is observe-only
  // and UserPromptSubmit can only block. Both stash their context instead, and the prompt entry,
  // also registered on PostToolUse, delivers it beside the next tool result.
  additionalHooks: [{ event: "PostToolUse", entry: "pre-prompt.mjs", timeout: 30 }],
  sessionStart: {
    ...standardSessionStart("grok-build"),
    // Grok's wire envelope is camelCase, unlike Claude's similarly named hook events.
    parse: (ev) => ({
      cwd: ev.cwd as string | undefined,
      sessionId: ev.sessionId as string | undefined,
    }),
    deferContextToToolHook: true,
  },
  prompt: {
    ...claudePrompt,
    harness: "grok-build",
    toolHook: {
      // Grok names the event twice: snake_case `hookEventName`, plus a Claude-named alias.
      accept: (ev) =>
        ev.hookEventName === "post_tool_use" || ev.hook_event_name === "PostToolUse",
      emit: (context) => ({
        hookSpecificOutput: { hookEventName: "PostToolUse", additionalContext: context },
      }),
    },
    parse: (ev) => ({
      prompt: ev.prompt as string | undefined,
      cwd: ev.cwd as string | undefined,
      sessionId: ev.sessionId as string | undefined,
    }),
  },
  retain: {
    hostTimeoutSec: 60,
    harness: "grok-build",
    parse: (ev) => ({
      sessionId: ev.sessionId as string | undefined,
      transcriptPath:
        typeof ev.cwd === "string" && typeof ev.sessionId === "string"
          ? grokTranscriptPath(ev.cwd, ev.sessionId)
          : undefined,
      cwd: ev.cwd as string | undefined,
    }),
    readTranscript: readGrokTranscript,
  },
};

export const qwenCode: HookHarnessSpec = {
  configStyle: "nested",
  timeoutUnit: "milliseconds",
  // TIMEOUTS ARE MILLISECONDS HERE, not seconds like every other harness in this table: Qwen's
  // hookRunner does `setTimeout(..., hookConfig.timeout ?? DEFAULT_HOOK_TIMEOUT)` with
  // DEFAULT_HOOK_TIMEOUT = 60_000 ("Timeout in milliseconds, default 60000"). Writing 30/60
  // registers 30ms/60ms hooks, which die before a Node process starts — and Qwen spawns hooks
  // `detached` and terminates the whole process TREE on timeout, so the retain is genuinely lost
  // rather than merely orphaned. `retain.hostTimeoutSec` below stays SECONDS, as its name says;
  // these two numbers are the same budget in different units for this harness alone.
  // The prompt timeout must also stay above core/config's DEFAULT_REFLECT_TIMEOUT_MS.
  install: {
    sessionStart: { event: "SessionStart", entry: "session-start.mjs", timeout: 30_000 },
    prompt: { event: "UserPromptSubmit", entry: "pre-prompt.mjs", timeout: 30_000 },
    stop: { event: "Stop", entry: "stop.mjs", timeout: 60_000 },
  },
  sessionStart: standardSessionStart("qwen-code"),
  prompt: qwenPrompt,
  retain: {
    hostTimeoutSec: 60,
    harness: "qwen-code",
    parse: (ev) => ({
      sessionId: ev.session_id as string | undefined,
      // Qwen supplies the path, but as the EMPTY STRING (not null, not absent) when chat
      // recording is off — runRetainHook's `if (!transcriptPath) return` already covers that.
      transcriptPath: ev.transcript_path as string | undefined,
      cwd: ev.cwd as string | undefined,
    }),
    readTranscript: readQwenTranscript,
  },
};

export const factoryDroid: HookHarnessSpec = {
  configStyle: "nested",
  install: {
    sessionStart: { event: "SessionStart", entry: "session-start.mjs", timeout: 30 },
    prompt: { event: "UserPromptSubmit", entry: "pre-prompt.mjs", timeout: 30 },
    stop: { event: "Stop", entry: "stop.mjs", timeout: 60 },
  },
  // Droid emits Notification(idle_prompt), not Stop, after user cancellation. Reusing the
  // idempotent retain entry point captures that final partial turn; its event gate ignores every
  // other notification type before config work begins.
  additionalHooks: [{ event: "Notification", entry: "stop.mjs", timeout: 60 }],
  sessionStart: standardSessionStart("factory-droid"),
  prompt: droidPrompt,
  retain: {
    hostTimeoutSec: 60,
    harness: "factory-droid",
    accept: (ev) =>
      ev.hook_event_name === "Stop" ||
      (ev.hook_event_name === "Notification" && ev.notification_type === "idle_prompt"),
    parse: (ev) => ({
      sessionId: ev.session_id as string | undefined,
      transcriptPath: ev.transcript_path as string | undefined,
      cwd: ev.cwd as string | undefined,
    }),
    readTranscript: readDroidTranscript,
  },
};
