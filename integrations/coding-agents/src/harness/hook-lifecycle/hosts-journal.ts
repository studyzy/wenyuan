/**
 * The journal-retaining hosts: ZCode, TraeCode and Kimi Code keep no durable transcript the Stop
 * hook can read, so the prompt hook records the user turn in the plugin's own journal
 * (core/turn-journal.ts) and the Stop hook closes it with the reply carried on its event.
 */
import type { HookHarnessSpec } from "./types";
import { claudePrompt, zcodeSessionId } from "./prompts";
import { standardSessionStart } from "./session-starts";
import { ensureTraecodeWorkspaceMcp } from "../../core/traecode-mcp";
import { zcodeAssistantText } from "../../core/transcript-zcode";
import { kimiPromptText, kimiSessionDir, readKimiTranscript } from "../../core/transcript-kimi";

export const zcode: HookHarnessSpec = {
  configStyle: "process",
  // ZCode's `timeoutMs`, so MILLISECONDS — see the qwen-code note for the same trap. The
  // prompt budget must stay above core/config's DEFAULT_REFLECT_TIMEOUT_MS.
  timeoutUnit: "milliseconds",
  install: {
    sessionStart: { event: "SessionStart", entry: "session-start.mjs", timeout: 30_000 },
    prompt: { event: "UserPromptSubmit", entry: "pre-prompt.mjs", timeout: 30_000 },
    stop: { event: "Stop", entry: "stop.mjs", timeout: 60_000 },
  },
  sessionStart: {
    ...standardSessionStart("zcode"),
    parse: (ev) => ({ cwd: ev.cwd as string | undefined, sessionId: zcodeSessionId(ev) }),
  },
  prompt: {
    ...claudePrompt,
    harness: "zcode",
    journalPrompt: true,
    parse: (ev) => ({
      prompt: (ev.prompt as string | undefined) ?? (ev.user_prompt as string | undefined),
      cwd: ev.cwd as string | undefined,
      sessionId: zcodeSessionId(ev),
    }),
  },
  retain: {
    hostTimeoutSec: 60,
    harness: "zcode",
    // No transcriptPath: the journal supplies it (see `journal` below).
    parse: (ev) => ({ sessionId: zcodeSessionId(ev), cwd: ev.cwd as string | undefined }),
    journal: {
      // `responseText` is the full reply and is what ZCode sends in practice. The ephemeral
      // transcript is the fallback when it is absent, and `responsePreview` — which is
      // TRUNCATED — is the last resort, preferred only over losing the turn entirely.
      assistantText: (ev) =>
        (
          (ev.responseText as string | undefined) ||
          zcodeAssistantText(ev.transcript_path as string | undefined) ||
          (ev.responsePreview as string | undefined) ||
          ""
        ).trim(),
    },
  },
};

export const traecode: HookHarnessSpec = {
  configStyle: "nested",
  install: {
    sessionStart: { event: "SessionStart", entry: "session-start.mjs", timeout: 30 },
    prompt: { event: "UserPromptSubmit", entry: "pre-prompt.mjs", timeout: 30 },
    stop: { event: "Stop", entry: "stop.mjs", timeout: 60 },
  },
  sessionStart: {
    ...standardSessionStart("traecode"),
    // TraeCode wires its per-workspace MCP access through a workspace settings file, not a
    // user config: the session itself is the right moment to keep that registration current
    // (merge-not-clobber, our entry only), because an opted-out repo must gain nothing.
    ensureMcpRegistration: (cwd) => ensureTraecodeWorkspaceMcp(cwd),
  },
  prompt: {
    ...claudePrompt,
    harness: "traecode",
    journalPrompt: true,
    // TraeCode's UserPromptSubmit payload carries `prompt` exactly like Claude Code's.
    parse: (ev) => ({
      prompt: ev.prompt as string | undefined,
      cwd: ev.cwd as string | undefined,
      sessionId: ev.session_id as string | undefined,
    }),
  },
  retain: {
    hostTimeoutSec: 60,
    harness: "traecode",
    // No transcriptPath: the journal supplies it (see `journal` below).
    parse: (ev) => ({
      sessionId: ev.session_id as string | undefined,
      cwd: ev.cwd as string | undefined,
    }),
    journal: {
      // `last_assistant_message` is the full reply — TraeCode's `responseText`.
      assistantText: (ev) => ((ev.last_assistant_message as string | undefined) ?? "").trim(),
    },
  },
};

export const kimiCode: HookHarnessSpec = {
  // Kimi's ~/.kimi-code/config.toml takes a FLAT [[hooks]] array whose entries are validated by a
  // strict 4-key schema (event/matcher/command/timeout). Neither JSON style can express it, and a
  // fifth key drops EVERY hook in the file at warning severity, so its installer writes the block
  // itself (the grok-build pattern) rather than going through the generic merge.
  configStyle: "toml-array",
  install: {
    // Timeouts are SECONDS here (integer 1-600, default 30) — the same unit as every other
    // supported host, and the opposite of qwen-code's identically named field.
    sessionStart: { event: "SessionStart", entry: "session-start.mjs", timeout: 30 },
    prompt: { event: "UserPromptSubmit", entry: "pre-prompt.mjs", timeout: 30 },
    stop: { event: "Stop", entry: "stop.mjs", timeout: 60 },
  },
  sessionStart: {
    harness: "kimi-code",
    parse: (ev) => ({
      cwd: ev.cwd as string | undefined,
      sessionId: ev.session_id as string | undefined,
    }),
    // Kimi runs SessionStart hooks for their side effects only: it awaits the trigger and drops
    // the result, so nothing emitted here can reach the model or the terminal. Installed for the
    // seed/session-root work runSessionStartHook does, and silent like antigravity-cli's.
    emit: () => ({}),
  },
  prompt: {
    harness: "kimi-code",
    parse: (ev) => ({
      // A block array, not a string — the same shape as the wire log's `turn.prompt.input`.
      prompt: kimiPromptText(ev.prompt),
      cwd: ev.cwd as string | undefined,
      sessionId: ev.session_id as string | undefined,
    }),
    // Kimi has no additionalContext: a top-level `message` is the injection channel, which the
    // CLI wraps as <hook_result hook_event="UserPromptSubmit"> and appends to the conversation.
    // `notice` has no banner channel to reach (the copilot-cli precedent), but it cannot simply
    // be dropped: when a hook's stdout parses to JSON carrying no message, Kimi falls back to
    // injecting the RAW STDOUT, so an empty `{}` would put a literal "{}" in front of the model.
    // Emitting the notice on the notice-only turn is what keeps that from ever happening.
    emit: (context, notice) => ({ message: context || notice }),
  },
  retain: {
    hostTimeoutSec: 60,
    harness: "kimi-code",
    parse: (ev) => {
      const sessionId = ev.session_id as string | undefined;
      return {
        sessionId,
        // Kimi's Stop payload carries no transcript path — its hook feature has no such field at
        // all — so resolve the session's own directory from the id, as grok-build does. The
        // reader takes that DIRECTORY and reads every agent's wire.jsonl beneath it.
        transcriptPath: sessionId ? kimiSessionDir(sessionId) : undefined,
        cwd: ev.cwd as string | undefined,
      };
    },
    readTranscript: readKimiTranscript,
  },
};
