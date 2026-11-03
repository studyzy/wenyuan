/**
 * The single lifecycle contract for every hook-based harness. Payload parsing, host response
 * encoding and transcript readers resolve through this registry, and the installed event names and
 * budgets are declared on the same objects, so a harness cannot silently drift between the runtime
 * and the installer wiring. Adding a harness means adding one complete declaration.
 */
import type { HookSpec } from "../../core/hook";
import type { RetainHookSpec } from "../../core/retain-hook";
import type { SessionStartHookSpec } from "../../core/session-start";

/** One hook host's own lifecycle vocabulary, keyed by the harness name the registry stamps. */
export type HookHarnessName =
  | "claude-code"
  | "codex"
  | "antigravity-cli"
  | "cursor-cli"
  | "copilot-cli"
  | "devin-cli"
  | "grok-build"
  | "dcode"
  | "qwen-code"
  | "factory-droid"
  | "zcode"
  | "traecode"
  | "kimi-code"
  | "workbuddy"
  | "codebuddy";

/** The three lifecycle stages every hook host installs. */
export type HookLifecycle = "sessionStart" | "prompt" | "stop";

/**
 * How the HOST spells one hook registration.
 *   nested  — Claude Code's matcher group: `[{hooks:[{type:"command", command, timeout}]}]`.
 *   flat    — one `{command, timeout}` object per entry (Cursor, Copilot, Antigravity).
 *   process — ZCode's: a matcher group like `nested`, but the command is SPLIT into an argv
 *             (`type:"process"`, `command:"node"`, `args:[...]`) and the budget is `timeoutMs`.
 *             The split matters: ZCode spawns without a shell, so a quoted command string is
 *             looked up verbatim as an executable name and never runs.
 *   toml-array — Kimi Code's flat `[[hooks]]` array of tables, whose entry schema is strict
 *             (event/matcher/command/timeout); a fifth key drops EVERY hook in the file, so
 *             its installer writes the block itself. See the installer's kimi adapter.
 */
export type HookConfigStyle = "nested" | "flat" | "process" | "toml-array";

export interface HookInstallSpec {
  event: string;
  /** The shared staged hook shim this registration runs (dist/runtime/hooks/*.mjs). One dispatch
   *  path for every host — the per-host payload shape lives in hosts/hook-shape.ts, not in the
   *  script name, so a new host is a registration entry rather than a new bin. */
  entry: string;
  /** In `HookHarnessSpec.timeoutUnit` — NOT always seconds. See that field. */
  timeout?: number;
}

/**
 * The unit the HOST reads `HookInstallSpec.timeout` in. Every host but Qwen Code uses seconds;
 * Qwen passes the value straight to setTimeout, so 30 there means 30ms and the hook is dead before
 * a Node process starts. Declaring the unit makes that difference checkable instead of a comment:
 * the lifecycle test normalises through it, so changing Qwen's 30_000 to 30 without also changing
 * this field now fails a test rather than silently shipping dead hooks.
 */
export type HookTimeoutUnit = "seconds" | "milliseconds";

export interface HookHarnessSpec {
  configStyle: HookConfigStyle;
  /** Defaults to "seconds" when absent — the unit every host but qwen-code uses. */
  timeoutUnit?: HookTimeoutUnit;
  install: Record<HookLifecycle, HookInstallSpec>;
  /** Native host events beyond the shared three-stage lifecycle. */
  additionalHooks?: HookInstallSpec[];
  sessionStart: SessionStartHookSpec;
  prompt: HookSpec;
  retain: RetainHookSpec;
}
