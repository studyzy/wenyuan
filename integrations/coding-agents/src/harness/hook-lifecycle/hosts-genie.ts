/**
 * The two hosts built on the `@genie/agent-cli` engine: identical hook protocol, different config
 * files and transcript schemas.
 */
import type { HookHarnessSpec } from "./types";
import { geniePrompt, genieRetain, genieSessionStart } from "./session-starts";
import { readCodebuddyTranscript } from "../../core/transcript-codebuddy-ide";
import { readWorkbuddyTranscript } from "../../core/transcript-workbuddy";

/**
 * WorkBuddy keeps a durable transcript at ~/.workbuddy/projects/<cwd-encoded>/<uuid>.jsonl and
 * speaks Claude's hook protocol — `session_id` / `transcript_path` / `cwd` in,
 * hookSpecificOutput + systemMessage out — apart from the two quirks genieSessionStart/geniePrompt
 * handle. The other host-specific piece is the transcript SCHEMA: `type:"message"` records
 * carrying top-level `role`/`content`, which core/transcript-workbuddy.ts normalizes. Its Stop
 * payload fills `transcript_path` for real (the engine's own path resolution), so it retains
 * through `readTranscript` like every other file-backed host rather than the journal ZCode needs.
 * CodeBuddy Code ships the same engine and reuses this reader (see `codebuddy`).
 */
export const workbuddy: HookHarnessSpec = {
  configStyle: "nested",
  install: {
    sessionStart: { event: "SessionStart", entry: "session-start.mjs", timeout: 30 },
    prompt: { event: "UserPromptSubmit", entry: "pre-prompt.mjs", timeout: 30 },
    stop: { event: "Stop", entry: "stop.mjs", timeout: 60 },
  },
  sessionStart: genieSessionStart("workbuddy"),
  prompt: geniePrompt("workbuddy"),
  retain: genieRetain("workbuddy", readWorkbuddyTranscript),
};

/**
 * CodeBuddy — the same `@genie/agent-cli` engine WorkBuddy ships, running under its own product
 * config (`~/.codebuddy`; WorkBuddy only renames the home folder). Hook names and payload shape
 * are identical, so this spec differs from `workbuddy` in the files the installer writes, the
 * harness name stamped on what it retains — and ONE thing more: CodeBuddy ships in two hosts whose
 * transcripts do NOT look alike. CodeBuddy Code (the CLI) writes the WorkBuddy JSONL, while the
 * IDE hands the Stop hook its own `<conversation>/index.json` directory layout. So the reader is
 * a dispatcher on the shape it was handed (core/transcript-codebuddy-ide.ts).
 */
export const codebuddy: HookHarnessSpec = {
  configStyle: "nested",
  install: {
    sessionStart: { event: "SessionStart", entry: "session-start.mjs", timeout: 30 },
    prompt: { event: "UserPromptSubmit", entry: "pre-prompt.mjs", timeout: 30 },
    stop: { event: "Stop", entry: "stop.mjs", timeout: 60 },
  },
  sessionStart: genieSessionStart("codebuddy"),
  prompt: geniePrompt("codebuddy"),
  retain: genieRetain("codebuddy", readCodebuddyTranscript),
};
