/**
 * The per-host prompt-hook specs: how each host's UserPromptSubmit-equivalent payload is read, and
 * how recalled context is wrapped back into the host's native hook-output schema.
 */
import type { HookSpec } from "../../core/hook";
import { readAntigravityTranscript } from "../../core/transcript-antigravity";

export const claudePrompt: HookSpec = {
  harness: "claude-code",
  parse: (ev) => ({
    prompt: ev.prompt as string | undefined,
    cwd: ev.cwd as string | undefined,
    sessionId: ev.session_id as string | undefined,
  }),
  emit: (context, notice) => ({
    ...(notice ? { systemMessage: notice } : {}),
    hookSpecificOutput: { hookEventName: "UserPromptSubmit", additionalContext: context },
  }),
};

export const codexPrompt: HookSpec = {
  ...claudePrompt,
  harness: "codex",
  parse: (ev) => ({
    prompt: (ev.prompt as string | undefined) ?? (ev.user_prompt as string | undefined),
    cwd: ev.cwd as string | undefined,
    sessionId: ev.session_id as string | undefined,
  }),
};

export const dcodePrompt: HookSpec = {
  ...claudePrompt,
  harness: "dcode",
};

/**
 * Factory Droid speaks Claude Code's hook protocol field for field: `session_id`, `transcript_path`
 * and `cwd` in, `hookSpecificOutput.additionalContext` + `systemMessage` out. Its user-level hook
 * config (`~/.factory/hooks.json`) also uses Claude's matcher-group shape, so the only real
 * difference from `claude-code` is where the installer writes the registration and which transcript
 * reader parses the file.
 */
export const droidPrompt: HookSpec = {
  ...claudePrompt,
  harness: "factory-droid",
};

/**
 * Qwen Code speaks Claude Code's hook protocol field for field (session_id / transcript_path / cwd
 * in, hookSpecificOutput.additionalContext + systemMessage out), so only `parse` differs — and it
 * reads `submitted_prompt`, NOT `prompt`.
 *
 * `UserPromptSubmit` fires on tool-result continuations too, not just on submissions: Qwen's send
 * loop labels the first turn `userQuery` and every continuation `toolResult`, and the hook's fire
 * guard excludes only retry/steer/cron/notification/teammate/goal. On a continuation `prompt` holds
 * whatever text is currently model-bound (a tool result), so keying on it would recall ~20 times per
 * user turn against tool output. `submitted_prompt` is attached only when the turn is BOTH the first
 * one and a real `userQuery`, which makes it the exact genuine-submission marker — and runHook's
 * `if (!prompt) return` then suppresses every continuation with no core change.
 *
 * Cost, deliberately accepted: `submitted_prompt` is the interactive TUI's text projection, so
 * headless (`qwen -p`), serve/SDK and ACP sessions carry none and never recall. They still get the
 * SessionStart seed and the Stop write-back.
 */
export const qwenPrompt: HookSpec = {
  ...claudePrompt,
  harness: "qwen-code",
  parse: (ev) => ({
    prompt: ev.submitted_prompt as string | undefined,
    cwd: ev.cwd as string | undefined,
    sessionId: ev.session_id as string | undefined,
  }),
};

/** ZCode sends `session_id` on UserPromptSubmit and BOTH spellings on Stop; accept either. */
export const zcodeSessionId = (ev: Record<string, unknown>): string | undefined =>
  (ev.session_id as string | undefined) ?? (ev.sessionId as string | undefined);

export const antigravityCwd = (ev: Record<string, unknown>): string | undefined =>
  Array.isArray(ev.workspacePaths) ? (ev.workspacePaths[0] as string | undefined) : undefined;

export const antigravityPrompt: HookSpec = {
  harness: "antigravity-cli",
  requireCwd: true,
  parse: (ev) => ({
    // Antigravity's PreInvocation payload deliberately omits the prompt. Its transcript is already
    // persisted at that point, so recover the latest real user turn from the supplied JSONL path.
    prompt: readAntigravityTranscript(ev.transcriptPath as string | undefined)
      .filter((turn) => turn.role === "user")
      .at(-1)?.content,
    cwd: antigravityCwd(ev),
    sessionId: ev.conversationId as string | undefined,
  }),
  emit: (context) => ({ injectSteps: context ? [{ ephemeralMessage: context }] : [] }),
};

const cursorCwd = (ev: Record<string, unknown>): string | undefined =>
  (ev.cwd as string | undefined) ??
  (ev.workspace_root as string | undefined) ??
  (Array.isArray(ev.workspace_roots) ? (ev.workspace_roots[0] as string | undefined) : undefined);

export const cursorPrompt: HookSpec = {
  harness: "cursor-cli",
  parse: (ev) => ({
    prompt: (ev.prompt as string | undefined) ?? (ev.user_prompt as string | undefined),
    cwd: cursorCwd(ev),
    sessionId: (ev.conversation_id as string | undefined) ?? (ev.session_id as string | undefined),
  }),
  emit: (context) => ({ continue: true, additional_context: context }),
};

/** Cursor reads the cwd out of three spellings; the session-start and retain specs share it. */
export const cursorCwdOf = cursorCwd;

export const copilotPrompt: HookSpec = {
  harness: "copilot-cli",
  parse: (ev) => ({
    prompt: ev.prompt as string | undefined,
    cwd: ev.cwd as string | undefined,
    sessionId: ev.sessionId as string | undefined,
  }),
  emit: (context, _notice, ev) => ({
    // Copilot's userPromptTransformed hook replaces model-facing content rather than appending
    // hook context. Preserve its transformed prompt and add the shared memory injection.
    modifiedTransformedPrompt:
      `${(ev?.transformedPrompt as string | undefined) ?? ""}\n\n${context}`.trim(),
  }),
};

// Devin does not set DEVIN_PROJECT_DIR in every spawn context. It still launches hooks from the
// project dir, so fall back to the process cwd; env-only resolution tagged those sessions
// `project:unknown`.
export const devinCwd = (): string => process.env.DEVIN_PROJECT_DIR || process.cwd();

export const devinPrompt: HookSpec = {
  harness: "devin-cli",
  requireCwd: true,
  parse: (ev) => ({
    prompt: ev.prompt as string | undefined,
    cwd: devinCwd(),
    sessionId: ev.session_id as string | undefined,
  }),
  emit: (context) => ({
    hookSpecificOutput: { hookEventName: "UserPromptSubmit", additionalContext: context },
  }),
};
