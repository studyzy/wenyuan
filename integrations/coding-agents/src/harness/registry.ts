/**
 * Harness registry. Add a coding agent by implementing HarnessAdapter (one file) and registering
 * it here — the backfill's --harness flag resolves through getHarness().
 *
 * getHarness() never statically OR dynamically imports the plugin adapters, even for "opencode":
 * backfill (its only real caller — see core/config.ts's note that the top-level `harness` key just
 * selects the backfill session formatter) only ever needs a harness's chatReader, and every
 * harness's chatReader — opencode included — is the same normalized-JSON reader (jsonChatReader
 * below). Each plugin's runtime is wired up directly by its own entrypoint under src/plugins/,
 * bypassing this registry entirely. That keeps this file — and everything bundled from it (in
 * particular backfill) — free of every host SDK.
 */
import { readFileSync } from "node:fs";
import type { ChatSession, HarnessAdapter } from "../core/types";

/** Every harness ingests past sessions through the same normalized JSON interchange format. */
export const jsonChatReader = (harness: string) => ({
  describe:
    `${harness} sessions via a normalized JSON export ` +
    "(--conversations file: [{ id, turns:[{role,text,timestamp?}] }])",
  async read(opts: { conversations?: string }): Promise<ChatSession[]> {
    if (!opts.conversations) return [];
    return JSON.parse(readFileSync(opts.conversations, "utf8")) as ChatSession[];
  },
});

/**
 * A harness whose runtime this registry never constructs — either because it's a per-prompt HOOK
 * host rather than a persistent plugin (core/hook.ts), or because a persistent plugin's runtime is
 * built directly by its own entrypoint, not via this registry.
 */
const noRuntimeAdapter = (name: string, hint: string): HarnessAdapter => ({
  name,
  chatReader: jsonChatReader(name),
  createRuntime() {
    throw new Error(hint);
  },
});

export const HARNESS_NAMES = [
  "opencode",
  // opencode v2 (binary `opencode2`) — a ground-up rewrite of the plugin API, loaded from the
  // package's root index.js (src/plugins/opencode2.ts). Like opencode it has NO hook binary.
  "opencode2",
  // Kilo CLI is an opencode fork loaded as a persistent plugin (src/plugins/kilo.ts), so like
  // opencode it has NO hook binary.
  "kilo",
  // Cline CLI loads its plugin bundle through its native plugin manager; file hooks cannot inject.
  "cline-cli",
  // DeepSeek Harness loads its bundle as a native Cordis plugin (src/plugins/dsh.ts). Its
  // Claude Code / Codex hook bridges are optional packages, so there is no hook binary to install.
  "dsh",
  // pi loads its bundle as an extension (src/plugins/pi.ts), and Prime Agent — a fork of pi —
  // loads its own the same way (src/plugins/prime-agent.ts). Neither has a hook binary.
  "pi",
  "prime-agent",
  // Everything below is a per-prompt HOOK host: the installer wires the shared staged hook bins
  // (dist/runtime/hooks/{session-start,pre-prompt,stop}.mjs) into each host's own config dialect.
  "claude-code",
  "cursor-cli",
  "codex",
  "dcode",
  "antigravity-cli",
  "devin-cli",
  "copilot-cli",
  "grok-build",
  "qwen-code",
  "factory-droid",
  "zcode",
  "traecode",
  "kimi-code",
  "workbuddy",
  "codebuddy",
];

/**
 * The hook hosts, resolved through the SHARED staged hook bins rather than one bin per host: every
 * registration names the same dist/runtime/hooks/{session-start,pre-prompt,stop}.mjs shims, with
 * the per-host payload shapes owned by the hosts layer (hosts/hook-shape.ts). One dispatch path,
 * and the verify machinery stays host-agnostic.
 */
const HOOK_HOSTS = new Set([
  "claude-code",
  "cursor-cli",
  "codex",
  "dcode",
  "antigravity-cli",
  "devin-cli",
  "copilot-cli",
  "grok-build",
  "qwen-code",
  "factory-droid",
  "zcode",
  "traecode",
  "kimi-code",
  "workbuddy",
  "codebuddy",
]);

/**
 * Where each persistent-plugin harness's runtime is actually built. Exported so a test can assert,
 * over the whole family, that each of those files reports the harness mapped to it here.
 */
export const PLUGIN_ENTRYPOINTS: Record<string, string> = {
  opencode: "src/plugins/opencode.ts",
  opencode2: "src/plugins/opencode2.ts",
  kilo: "src/plugins/kilo.ts",
  "cline-cli": "src/plugins/cline.ts",
  pi: "src/plugins/pi.ts",
  "prime-agent": "src/plugins/prime-agent.ts",
  dsh: "src/plugins/dsh.ts",
};

export async function getHarness(name: string): Promise<HarnessAdapter> {
  // The persistent-plugin harnesses: their runtime is built by their own plugin entrypoint (which
  // owns whatever host shapes that host needs), never via this registry. Backfill still resolves
  // them here for the chatReader.
  const entry = PLUGIN_ENTRYPOINTS[name];
  if (entry) {
    return noRuntimeAdapter(
      name,
      `${name}'s runtime is built by ${entry} (the ${name} plugin entrypoint), not via the harness registry`
    );
  }
  if (!HOOK_HOSTS.has(name)) {
    throw new Error(`unknown harness '${name}'. available: ${HARNESS_NAMES.join(", ")}`);
  }
  return noRuntimeAdapter(
    name,
    `'${name}' has no persistent plugin runtime — install its hook registration (the shared staged hook bins) instead`
  );
}
