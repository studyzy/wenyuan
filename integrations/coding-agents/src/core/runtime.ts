/**
 * Host adapter runtime for PERSISTENT-PLUGIN harnesses (every host that loads us once and keeps
 * us, rather than exec'ing a fresh hook process per event). It delegates SessionStart and prompt
 * behavior to the same core lifecycle as the hook harnesses; this class keeps only the
 * host-specific injection, toast, and incremental-transcript responsibilities.
 *
 * A harness adapter feeds it three normalized events and reads two values back:
 *   - seedIfCold(repoPath)          : plugin load -> cold-check auto-seed (the page preamble is
 *                                     NOT computed here; onPrompt builds it per session)
 *   - onPrompt(sessionId, prompt)   : each user turn -> recall + build this turn's injection
 *   - getInjection(sessionId)       : the system-prompt text to inject this turn (or undefined)
 *   - toolSpecs()                   : the wenyuan_* knowledge/recall tools to register natively
 *   - onTranscript(sessionId, turns, lastTurnComplete): full transcript -> write back (on by default)
 *   - onSessionIdle(sessionId)      : assistant finished -> refetch + write back the completed
 *                                     exchange (the Stop-equivalent these hosts lack)
 *   - flush()                       : host is exiting -> wait (bounded) for in-flight write-backs
 * No host specifics live here — only the memory logic.
 */
import type { Config } from "./config";
import { maybeAutoUpdate } from "./auto-update";
import { diag } from "./diag";
import { describeError, log, setLogLevel } from "./log";
import type { MemoryClient } from "./client";
import { buildKnowledgePreamble } from "./knowledge-injection";
import { buildKnowledgeTools, type ToolSpec } from "./knowledge-tools";
import { buildPageTrigger } from "./missions";
import { retainLiveSession } from "./chat";
import type { TransportTurn } from "./transcript";
import { memoryCursorStore } from "./retain-cursor";
import { memoryUsageCursorStore, recordUsage } from "./usage";
import { buildRetainStamp } from "./retain-stamp";
import { buildSessionStartContext } from "./session-start";
import { syncCompanionSkill } from "./skill-sync";
import { buildHookOutput } from "./hook";
import { sessionCacheFile, writeSessionCache } from "./session-cache";
import { syncStatus } from "./status";

const DEFAULT_HARNESS = "opencode";
/** How long `flush` holds a host's exit for in-flight write-backs: room for one retain request. */
const FLUSH_TIMEOUT_MS = 10_000;

export class RuntimeCore {
  private readonly injection = new Map<string, string>(); // sessionId -> this turn's injection block
  private readonly turnCount = new Map<string, number>(); // sessionId -> user-turn counter (cadence)
  private readonly sessionState = new Map<string, { startTs: string; retainedTurns: number }>();
  /** Live write-back cursors. In memory, unlike the hook harnesses': this host outlives the session. */
  private readonly cursors = memoryCursorStore();
  /** Turns already written to the usage log (core/usage.ts), per session. */
  private readonly usageCursors = memoryUsageCursorStore();
  /** Pulls a session's CURRENT transcript from the host (set by the adapter); see onSessionIdle. */
  private fetchTranscript?: (sessionId: string) => Promise<TransportTurn[]>;
  /** Write-backs started and not yet settled — what `flush` waits for. */
  private readonly inFlight = new Set<Promise<void>>();
  private lastInjection = ""; // most recent turn's injection block, keyed by nothing (see getInjection)
  private deferInitialReflect = false;
  /** Host-notice channel (toast via the adapter). Optional, fail-open. */
  private notify?: (title: string, message: string) => void;

  constructor(
    private readonly client: MemoryClient,
    private readonly bankId: string,
    private readonly cfg: Config,
    /**
     * Which persistent-plugin host loaded us. Scopes diagnostics, the session cache file and the
     * retained transcript's harness field, so one host's sessions don't masquerade as another's.
     * Defaults to opencode, the original (and canonical) plugin harness.
     */
    readonly harness: string = DEFAULT_HARNESS,
    /** Workspace root this host opened — the SAME directory bank resolution used, so a
     *  `{gitProject}` in retainTags names the repo the bank was derived from. */
    private readonly projectDir: string = process.cwd()
  ) {
    setLogLevel(cfg.logLevel);
  }

  setNotifier(notify: (title: string, message: string) => void): void {
    this.notify = notify;
  }

  /**
   * Supply a way to READ the session's transcript back from the host. Without it `onSessionIdle`
   * no-ops, so hosts that can't refetch keep the old (lagging) turn-driven write-back rather than
   * retaining a stale transcript.
   */
  setTranscriptSource(fetch: (sessionId: string) => Promise<TransportTurn[]>): void {
    this.fetchTranscript = fetch;
  }

  /** The wenyuan_* knowledge + recall tools, bound to this bank, for the harness to register natively. */
  toolSpecs(): ToolSpec[] {
    return buildKnowledgeTools(this.client, this.bankId, {
      repoDir: this.projectDir,
      harness: this.harness,
      pageTrigger: buildPageTrigger(this.cfg),
      // One code path for the status tool: the shared syncStatus closes over THIS client and
      // workspace, so dsh-style multi-workspace hosts answer about the repo they opened.
      syncStatus: (bankId, repoDir) => syncStatus(this.client, bankId, repoDir),
      reflectTimeoutMs: this.cfg.reflectToolTimeoutMs,
      reflectBudget: this.cfg.reflectBudget,
      toolGuideExtra: this.cfg.toolGuideExtra,
      stampFor: () =>
        buildRetainStamp(this.cfg, {
          directory: this.projectDir,
          harness: this.harness,
          bankId: this.bankId,
        }),
    });
  }

  get writeBackEnabled(): boolean {
    return this.cfg.retainSessions;
  }

  /**
   * Plugin load (SessionStart-equivalent): on a cold repo, deterministically start the background
   * git-log seed + codebase survey. Reuses the exact hook-harness logic
   * (`buildSessionStartContext`) so these hosts seed identically. Never throws.
   *
   * It deliberately does NOT keep that call's knowledge preamble. This runs once per PROCESS and
   * these hosts outlive every session, so a roster captured here is stale for every session but
   * the first — `onPrompt` builds its own from the session's live page list.
   */
  async seedIfCold(repoPath: string | undefined): Promise<void> {
    // Anti-recursion: a headless survey session runs the agent (which loads this plugin) under the
    // hooks guard — the tools stay registered (toolSpecs, so the survey can ingest), but
    // seeding/recall/write-back must no-op or the survey would re-seed itself (see core/survey.ts).
    if (process.env.WENYUAN_DISABLE_HOOKS) return;
    // Companion skill, the same housekeeping `runSessionStartHook` does for hook harnesses — but
    // with `install`, because a persistent-plugin host can be wired by its OWN plugin manager, a
    // route our installer never sees, leaving the plugin loaded with its tools registered and no
    // skill on disk at all. No-op for a host with no skills directory.
    syncCompanionSkill(this.harness, { install: true });
    // Same session-start housekeeping the hook harnesses do in `runSessionStartHook`: a persistent
    // plugin host is a session start too, and leaving it out would mean these users never got an
    // update. Detached and rate-limited to once a day; the update lands for the NEXT session.
    void maybeAutoUpdate(this.cfg);
    try {
      const out = await buildSessionStartContext({
        cwd: repoPath || process.cwd(),
        bankId: this.bankId,
        cfg: this.cfg,
        client: this.client,
        harness: this.harness,
      });
      // The seed note is user-facing; these hosts have no visible system channel at load, so
      // surface it on the log (shows in the plugin log / console) rather than dropping it.
      // Banner: via the host's toast API when available (stderr corrupts the TUI); always logged.
      if (out.systemMessage) {
        const plain = out.systemMessage.replace(/\x1b\[[0-9;]*m/g, "");
        log.info(this.harness, plain);
        this.notify?.("WenYuan", plain.replace(/^WenYuan is /, "Is "));
      }
      this.deferInitialReflect = out.deferInitialReflect === true;
    } catch {
      /* seeding is best-effort — a cold-check failure never breaks the agent */
    }
  }

  /**
   * Each user turn delegates to the same `buildHookOutput` used by hook harnesses. The cache file
   * intentionally replaces any in-memory reflect/page state, keeping lifecycle behavior identical
   * across hosts while this adapter retains only delivery-specific state.
   */
  async onPrompt(sessionId: string | undefined, prompt: string): Promise<void> {
    if (process.env.WENYUAN_DISABLE_HOOKS) return; // anti-recursion (see seedIfCold)
    if (!sessionId || !prompt.trim()) return;
    const turns = (this.turnCount.get(sessionId) ?? 0) + 1;
    this.turnCount.set(sessionId, turns);

    const cacheFile = sessionCacheFile(this.harness, sessionId);
    if (this.deferInitialReflect) {
      // `seedIfCold` has no session id. Transfer its SessionStart decision to the first concrete
      // session here; `buildHookOutput` consumes it exactly once, like hook harnesses do.
      this.deferInitialReflect = false;
      writeSessionCache(cacheFile, { deferInitialReflect: true });
    }
    const output = await buildHookOutput({
      harness: this.harness,
      prompt,
      cfg: this.cfg,
      client: this.client,
      cacheFile,
    });

    const blocks: string[] = [];
    // The SessionStart-equivalent preamble, injected once on the first turn (later turns get the
    // periodic refresh below). Built HERE, from this session's own roster, rather than reused from
    // plugin load: that one is a process-lifetime snapshot, so a host started before the pages
    // existed told every later session "No knowledge pages yet" while the same turn's memory block
    // listed pages by id.
    if (turns === 1) {
      blocks.push(
        buildKnowledgePreamble(output.pages, {
          reflectOnNewGoals: this.cfg.autoInject !== "reflect",
          extra: this.cfg.toolGuideExtra,
        })
      );
    }
    if (output.context) blocks.push(output.context);
    // These hosts have no user-message hook channel; use the native toast instead of stderr, which
    // renders inside the TUI input line. The shared output owns when a notice exists.
    if (output.notice) {
      const preview = output.notice.replace(/\s+/g, " ").trim().slice(0, 140);
      log.info(this.harness, "reflect goal", { preview });
      this.notify?.("WenYuan · recalled past decisions", preview);
    }
    const block = blocks.filter(Boolean).join("\n\n");
    this.injection.set(sessionId, block);
    this.lastInjection = block; // for consumers that can't supply a sessionId (see getInjection)
  }

  /**
   * The system-prompt text to inject this turn (built by the preceding onPrompt), or undefined.
   * Some hosts fire the system-prompt transform with NO sessionId (input is just `{model}`), so a
   * session-keyed lookup returns nothing there — fall back to the most recent turn's block
   * (`lastInjection`). The completion's transform fires right after that session's message
   * event/onPrompt, so `lastInjection` is this turn's block.
   */
  getInjection(sessionId: string | undefined): string | undefined {
    const keyed = sessionId ? this.injection.get(sessionId) : undefined;
    return keyed ?? this.lastInjection ?? undefined;
  }

  /**
   * Full normalized transcript (rich: user/assistant text + tool calls/outputs): upsert every turn
   * when enabled. On by default for persistent-plugin hosts (parity with hook-harness Stop
   * write-back), so their sessions compound into memory; opt out with `retainSessions: false`.
   *
   * There is deliberately no client-side cadence. Batching write-backs meant holding turns in a
   * process the host can close at any moment, with no reliable signal on the way out — the flush a
   * cadence needs would have to ride a session-end hook, and those are cancelled at shutdown. The
   * server coalesces instead: queued retains for one document fold into a single execution, so
   * submitting every turn costs one extraction, not one per turn, and nothing is ever held
   * somewhere it can be lost.
   */
  async onTranscript(
    sessionId: string,
    turns: TransportTurn[],
    /** Whether the agent has finished answering the last prompt. Required, not defaulted: some
     *  hosts hand this over while BUILDING a request (false) and others after the run ends (true),
     *  and a wrong default records every turn one late and never the session's last. */
    lastTurnComplete: boolean
  ): Promise<void> {
    if (process.env.WENYUAN_DISABLE_HOOKS) return; // anti-recursion (see seedIfCold)
    if (!sessionId || !turns.length) return;
    // Usage is a local file, so it is recorded whether or not write-back is on.
    this.recordUsage(sessionId, turns, lastTurnComplete);
    if (!this.writeBackEnabled) return;
    const st = this.stateFor(sessionId);
    this.retain(sessionId, turns, st.startTs);
  }

  /**
   * Session went idle — the assistant has finished replying.
   *
   * This is the Stop-equivalent these hosts lack. `onTranscript` alone is driven by a
   * message transform the host runs while BUILDING a request, so the transcript it hands us never
   * contains the reply that request produced: write-back always lagged one user turn, and a
   * session's final exchange was never retained at all. Refetching here (rather than replaying a
   * cached list) is the whole point — the cached one predates the reply.
   *
   * Cadence is deliberately bypassed: idle means "there may be nothing after this", so a pending
   * turn must not wait for a threshold it will never reach.
   */
  async onSessionIdle(sessionId: string): Promise<void> {
    if (process.env.WENYUAN_DISABLE_HOOKS) return; // anti-recursion (see seedIfCold)
    if (!sessionId || !this.fetchTranscript) return;
    let turns: TransportTurn[];
    try {
      turns = await this.fetchTranscript(sessionId);
    } catch (e) {
      diag(this.harness, "idle_fetch_failed", {
        error: describeError(e),
        session: sessionId,
      });
      return;
    }
    if (!turns.length) return;
    this.recordUsage(sessionId, turns, true); // idle: the reply is in
    if (!this.writeBackEnabled) return; // usage above is local and recorded regardless
    const st = this.stateFor(sessionId);
    // idle can fire more than once for one exchange (and again on a session with no new activity);
    // only retain when this transcript actually grew past what we last wrote.
    if (turns.length <= st.retainedTurns) return;
    st.retainedTurns = turns.length;
    this.retain(sessionId, turns, st.startTs, "idle");
  }

  private recordUsage(sessionId: string, turns: TransportTurn[], lastTurnComplete: boolean): void {
    recordUsage({
      harness: this.harness,
      sessionId,
      bankId: this.bankId,
      turns,
      cursors: this.usageCursors,
      lastTurnComplete,
      // No `reviseLastTurn` here, unlike the hook path: this runtime never records a turn before
      // its reply exists. `onTranscript` is handed `false` while the host is still building the
      // request, which holds the turn back, and `onSessionIdle` refetches a transcript that
      // includes the reply before recording it.
    });
  }

  private stateFor(sessionId: string): {
    startTs: string;
    retainedTurns: number;
  } {
    let st = this.sessionState.get(sessionId);
    if (!st) {
      st = { startTs: new Date().toISOString(), retainedTurns: 0 };
      this.sessionState.set(sessionId, st);
    }
    return st;
  }

  /**
   * Wait for every write-back still in flight, at most `timeoutMs`. Write-backs stay fire-and-forget
   * so a turn never blocks on the network; a host that announces its exit calls this so the
   * session's final retain is not dropped with the process. Bounded so a stalled server cannot
   * hold the host's exit open. Never throws.
   */
  async flush(timeoutMs = FLUSH_TIMEOUT_MS): Promise<void> {
    if (!this.inFlight.size) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    await Promise.race([
      Promise.all(this.inFlight),
      new Promise<void>((r) => {
        timer = setTimeout(r, timeoutMs);
      }),
    ]);
    clearTimeout(timer);
  }

  /** Fire-and-forget upsert of the session transcript; never throws into the host. */
  private retain(
    sessionId: string,
    turns: TransportTurn[],
    startTs: string,
    trigger = "turn"
  ): void {
    const t0 = Date.now();
    const write = retainLiveSession(this.client, sessionId, turns, startTs, this.harness, {
      cursors: this.cursors,
      stamp: buildRetainStamp(this.cfg, {
        directory: this.projectDir,
        harness: this.harness,
        bankId: this.bankId,
        sessionId,
      }),
    })
      .then(() =>
        diag(this.harness, "retain_ok", {
          ms: Date.now() - t0,
          turns: turns.length,
          session: sessionId,
          trigger,
        })
      )
      .catch((e) =>
        diag(this.harness, "retain_failed", {
          ms: Date.now() - t0,
          error: describeError(e),
          session: sessionId,
          trigger,
        })
      );
    this.inFlight.add(write);
    void write.finally(() => this.inFlight.delete(write));
  }
}
