/**
 * Shared entrypoint factory for the PERSISTENT-PLUGIN harnesses (opencode and its forks).
 *
 * Unlike the per-prompt hook hosts — a fresh hook process per event — these harnesses load a
 * plugin once and keep it alive, so the whole v2 surface (recall+inject, cold seed, native tools,
 * write-back) is delivered through the host's plugin hooks. Kilo CLI is a fork of opencode: same
 * config schema, same `plugin` array, so both hosts drive the identical runtime and differ only in
 * which harness name they report. That name is NOT config-chosen — the host that loaded us
 * determines it — and it selects the `harnesses.<name>` config section and feeds `{harness}` bank
 * templating.
 *
 * Every host shape here is STRUCTURALLY typed: this package takes no dependency on the host's
 * plugin SDK, so there is nothing for a package manager to resolve inside a profile and no version
 * to keep in step — any host whose plugin contract still matches can load this file.
 */
import { posix, win32 } from "node:path";
import { resolveHostMemory } from "../core/host-client";
import { log } from "../core/log";
import { RuntimeCore } from "../core/runtime";
import { readOpencodeMessages, type OcMessage } from "../core/transcript-opencode";
import { opencodeAdapter } from "./opencode";

/** Hold toasts until this long after init — see the notifier comment below. */
const TOAST_MOUNT_GRACE_MS = 3000;

function isFilesystemRoot(path: string): boolean {
  return posix.parse(path).root === path || win32.parse(path).root === path;
}

/** Prefer the worktree a session runs in; a bare filesystem root names no repository. */
export function resolveProjectDirectory(input?: {
  worktree?: string;
  directory?: string;
}): string | undefined {
  const worktree = input?.worktree;
  return worktree && !isFilesystemRoot(worktree) ? worktree : input?.directory;
}

/** The host-injected SDK client, reduced to the two channels this factory reads. */
export interface OcPluginClient {
  /** Toast publishing (POST /tui/show-toast style): resolves {data|error}, openapi-fetch style. */
  tui?: { showToast?: (o: unknown) => Promise<unknown> };
  /** Session reads: returns the session's {info, parts} message items. */
  session?: {
    messages?: (o: { path: { id: string } }) => Promise<{ data?: OcMessage[]; error?: unknown }>;
  };
}

/** What the host hands a v1 plugin when it loads it. */
export interface OcPluginInput {
  worktree?: string;
  directory?: string;
  client?: OcPluginClient;
}

/** A v1 plugin: a function returning the host's named-hooks bag. */
export type OcPlugin = (input?: OcPluginInput) => Promise<Record<string, unknown>>;

/**
 * Build the default export for a persistent-plugin host. `harness` is the name the host is known
 * by ("opencode", "kilo"), used for config lookup, bank derivation and log scoping.
 */
export function createPluginEntry(harness: string): OcPlugin {
  return async (input) => {
    const projectDir = resolveProjectDirectory(input);
    const dir = projectDir || process.cwd();
    const { cfg, bankId, client } = resolveHostMemory(harness, dir);
    // Global switch, per-bank opt-out and optInOnly all land here: inert plugin, same agent, no
    // memory (baseline parity).
    if (cfg.disabled) return {};
    const core = new RuntimeCore(client, bankId, cfg, harness, dir);
    // Visible presence via the host's own notice API — never stderr, which these TUIs render at
    // the cursor, wedging text against the input bar.
    const showToast = input?.client?.tui?.showToast;
    // The host resolves the call with {data|error} instead of rejecting — inspect the result, not
    // .catch.
    log.debug(harness, "toast channel", { wired: Boolean(showToast) });
    if (showToast) {
      // The toast event is not durable: one published before the TUI mounts and subscribes to the
      // event stream is silently lost. A warm bank makes the seed banner fire <1s after plugin
      // init, well before mount — so hold any toast until ~3s past init (later ones go immediately).
      const initAt = Date.now();
      core.setNotifier((title, message) => {
        const wait = Math.max(0, TOAST_MOUNT_GRACE_MS - (Date.now() - initAt));
        setTimeout(() => {
          void showToast({ body: { title, message, variant: "info", duration: 6000 } })
            .then((r: unknown) => {
              const err = (r as { error?: unknown })?.error;
              if (err)
                log.debug(harness, "toast rejected", { error: JSON.stringify(err).slice(0, 200) });
            })
            .catch((e: unknown) =>
              log.debug(harness, "toast failed", { error: String(e).slice(0, 120) })
            );
        }, wait).unref?.();
      });
    }

    // Let the runtime READ the transcript back on session.idle. The messages.transform hook only
    // ever sees the list as it stood BEFORE the reply, so the completed exchange has to be
    // refetched; the session message read returns the same {info, parts} items the live-transcript
    // normalizer already consumes.
    const readMessages = input?.client?.session?.messages;
    if (readMessages) {
      core.setTranscriptSource(async (sessionId) => {
        const r = await readMessages({ path: { id: sessionId } });
        if (r?.error) throw new Error(JSON.stringify(r.error).slice(0, 200));
        return readOpencodeMessages(r?.data ?? []);
      });
    }
    log.debug(harness, "transcript source", { wired: Boolean(readMessages) });

    const runtime = opencodeAdapter.createRuntime(core) as Record<string, unknown>;

    // SessionStart-equivalent: cold-check the bank and kick off the background engine.
    // Fire-and-forget — these hosts BLOCK THEIR BOOT on plugin init, so this must never gate
    // startup on a network round-trip (a stalled server froze the whole TUI); onPrompt needs
    // nothing from it, building its own preamble from the session's page list.
    void core.seedIfCold(projectDir);

    // Keeping the bank current needs no separate path: seedIfCold fired the deepen engine, and the
    // engine's idempotent git pass (cfg.gitIngest) ingests whatever is new — syncing IS re-seeding.
    return runtime;
  };
}
