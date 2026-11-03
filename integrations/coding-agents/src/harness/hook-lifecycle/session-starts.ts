/**
 * Session-start spec factories and the retain spec factory shared by the Claude-protocol hosts, plus
 * the families that need small variations of them.
 */
import type { HookSpec } from "../../core/hook";
import type { RetainHookSpec } from "../../core/retain-hook";
import type { SessionStartHookSpec } from "../../core/session-start";
import { claudePrompt } from "./prompts";

export const standardSessionStart = (harness: string): SessionStartHookSpec => ({
  harness,
  parse: (ev) => ({
    cwd: ev.cwd as string | undefined,
    sessionId: ev.session_id as string | undefined,
  }),
  emit: (out) => ({
    ...(out.systemMessage ? { systemMessage: out.systemMessage } : {}),
    hookSpecificOutput: {
      hookEventName: "SessionStart",
      ...(out.additionalContext ? { additionalContext: out.additionalContext } : {}),
    },
  }),
});

/**
 * WorkBuddy and CodeBuddy share the `@genie/agent-cli` engine, which differs from Claude Code in two
 * ways that matter here (both read from its bundle):
 *   - when a hook's output carries no `hookSpecificOutput.additionalContext`, the engine pastes the
 *     hook's RAW stdout into the model's context — our JSON, ANSI-coloured banner and all.
 *     `suppressOutput` turns that fallback off; `additionalContext` still reaches the model and
 *     `systemMessage` is still shown to the user.
 *   - its SessionStart payload has no `cwd`; the engine sets CODEBUDDY_PROJECT_DIR on every hook.
 */
export const genieSessionStart = (harness: string): SessionStartHookSpec => {
  const base = standardSessionStart(harness);
  return {
    ...base,
    parse: (ev) => ({
      cwd: (ev.cwd as string | undefined) || process.env.CODEBUDDY_PROJECT_DIR,
      sessionId: ev.session_id as string | undefined,
    }),
    emit: (out) => ({ ...(base.emit(out) as object), suppressOutput: true }),
  };
};

export const geniePrompt = (harness: string): HookSpec => ({
  ...claudePrompt,
  harness,
  emit: (context, notice, ev) => ({
    ...(claudePrompt.emit(context, notice, ev) as object),
    suppressOutput: true,
  }),
});

export const genieRetain = (
  harness: string,
  readTranscript: RetainHookSpec["readTranscript"]
): RetainHookSpec => ({
  hostTimeoutSec: 60,
  harness,
  parse: (ev) => ({
    sessionId: ev.session_id as string | undefined,
    transcriptPath: ev.transcript_path as string | undefined,
    cwd: ev.cwd as string | undefined,
  }),
  readTranscript,
});

/**
 * Grok Build also runs the hooks in ~/.claude/settings.json (its Claude compatibility layer), so
 * without this gate every Grok session would run Claude Code's hooks next to Grok's own: a second
 * SessionStart injection, a second recall per prompt, and a retain tagged claude-code. Grok's hook
 * runner sets the reserved GROK_HOOK_EVENT on every hook process; Claude Code never does.
 */
export const notGrokHosted = (): boolean => !process.env.GROK_HOOK_EVENT;
