/**
 * The single lifecycle contract for every hook-based harness. Entry-point wiring, payload parsing,
 * host response encoding, and transcript readers all resolve through this registry. Adding a
 * harness means adding one complete declaration here; its lifecycle cannot silently drift between
 * the runtime and the installer. The per-host specs are grouped by protocol family under
 * hook-lifecycle/ — this file only assembles and dispatches them.
 */
import { runHook } from "../core/hook";
import { runRetainHook } from "../core/retain-hook";
import { runSessionStartHook } from "../core/session-start";
import { codex, claudeCode, dcode, devinCli, factoryDroid, grokBuild, qwenCode } from "./hook-lifecycle/hosts-claude";
import { antigravityCli, copilotCli, cursorCli } from "./hook-lifecycle/hosts-flat";
import { kimiCode, traecode, zcode } from "./hook-lifecycle/hosts-journal";
import { codebuddy, workbuddy } from "./hook-lifecycle/hosts-genie";
import type {
  HookConfigStyle,
  HookHarnessName,
  HookHarnessSpec,
  HookInstallSpec,
  HookLifecycle,
  HookTimeoutUnit,
} from "./hook-lifecycle/types";

export type { HookConfigStyle, HookHarnessName, HookHarnessSpec, HookInstallSpec, HookLifecycle, HookTimeoutUnit };

export const HOOK_HARNESSES: Record<HookHarnessName, HookHarnessSpec> = {
  "claude-code": claudeCode,
  codex,
  dcode,
  "devin-cli": devinCli,
  "grok-build": grokBuild,
  "qwen-code": qwenCode,
  "factory-droid": factoryDroid,
  "antigravity-cli": antigravityCli,
  "cursor-cli": cursorCli,
  "copilot-cli": copilotCli,
  zcode,
  traecode,
  "kimi-code": kimiCode,
  workbuddy,
  codebuddy,
};

export const runHarnessSessionStart = (
  harness: HookHarnessName,
  preRead?: Record<string, unknown>,
): Promise<void> =>
  runSessionStartHook(HOOK_HARNESSES[harness].sessionStart, undefined, preRead);
export const runHarnessPrompt = (
  harness: HookHarnessName,
  preRead?: Record<string, unknown>,
): Promise<void> => runHook(HOOK_HARNESSES[harness].prompt, undefined, preRead);
export const runHarnessRetain = (
  harness: HookHarnessName,
  preRead?: Record<string, unknown>,
): Promise<void> => runRetainHook(HOOK_HARNESSES[harness].retain, undefined, preRead);
