#!/usr/bin/env node
/**
 * deepen — the background ingestion engine. NOT a user-facing CLI (no bin entry): the runtime
 * spawns it detached at every session start (core/seed.ts), and anyone needing deterministic
 * ingestion (benchmarks, live acceptance runs) executes it directly and polls the bank's status.
 *
 * IDEMPOTENT and RESUMABLE — safe to fire every session; each run does only the missing work:
 *   1. configure the bank (missions + retain strategies; PUT/PATCH, never a reset — a fresh bank
 *      IS the reset path)
 *   2. ingest conversations not yet in the bank (--conversations file, the normalized interchange
 *      JSON; dedup by the chat:<id> document id — live sessions arrive via write-back, so this is
 *      history import, not sync)
 *   3. seed the aggregated commit-message history (ONE cheap document) if absent
 *   4. progressively DEEPEN: ingest the next batch of not-yet-ingested commits individually with
 *      their full diffs, NEWEST first (recent decisions matter most), up to DIFF_BATCH per run and
 *      DEEPEN_DIFF_TARGET total — full precision arrives across sessions without a big-bang ingest
 *   5. drain this run's extractions; pages-last ordering makes a settled bank with zero active
 *      operations a real completion marker
 *
 * A per-bank heartbeat lease makes concurrent session starts a no-op.
 */
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { bankProjectName, deriveBankIdOrSkip } from "./bank";
import { ingestChats } from "./chat";
import { applyBankConfig, loadConfig } from "./config";
import { commitsSince, repoNameOf, retainCommit, syncGitLog } from "./git";
import { SURVEY_DOC_IDS } from "./survey";
import {
  acquireLease,
  heartbeatLease,
  LEASE_HEARTBEAT_MS,
  releaseLease,
} from "./survey-lease";
import { buildPageTrigger } from "./missions";
import { MemoryClient } from "./client";
import { DEEPEN_DIFF_TARGET } from "./status";
import type { ChatSession } from "./types";
import { pool } from "./util";
import { diag } from "./diag";
import { buildRetainStamp } from "./retain-stamp";
import { describeError, log as plog, setLogLevel } from "./log";

const DIFF_BATCH = 50; // per-run cap on per-commit diff ingestion (bounded session cost)

function arg(name: string, def?: string): string | undefined {
  const i = process.argv.indexOf(`--${name}`);
  if (i >= 0 && i + 1 < process.argv.length) return process.argv[i + 1];
  return process.argv.includes(`--${name}`) ? "true" : def;
}

const REPO = arg("repo");
const cfg0 = loadConfig({ harness: arg("harness") ?? undefined, path: arg("config") });
const BANK =
  arg("bank") ??
  (REPO ? deriveBankIdOrSkip(cfg0, REPO, arg("harness") ?? cfg0.harness) : cfg0.bankId) ??
  undefined;
const resolved0 = BANK
  ? applyBankConfig(cfg0, BANK, REPO ?? undefined)
  : { cfg: cfg0, bankId: BANK };
const cfg = resolved0.cfg;
const FINAL_BANK = resolved0.bankId ?? BANK;
if (cfg.disabled) {
  console.log(`deepen: bank ${FINAL_BANK} is disabled (banks override) — nothing to do`);
  process.exit(0);
}
const HARNESS = arg("harness") ?? cfg0.harness;
// The repository name the seeded knowledge pages are scoped to — see the `project` option below.
// `resolved0.bankId !== BANK` means a `banks.<id>.bank` rename redirected this run, and several
// repos may be renamed onto one destination, so the repo cannot claim to name it.
const PAGE_PROJECT = REPO && resolved0.bankId === BANK ? bankProjectName(cfg, REPO) : undefined;
const API_URL = arg("api-url") ?? cfg.apiUrl;
const API_TOKEN = arg("api-token") ?? cfg.apiToken;
const CONV = arg("conversations");
const GITLOG_LIMIT = arg("gitlog-limit") ? Number(arg("gitlog-limit")) : cfg.seedLimit;
// harness override (benchmarks/acceptance runs want deterministic depth regardless of user config)
const GIT_INGEST =
  (["message", "full", "none"] as const).find((m) => m === arg("git-ingest")) ?? cfg.gitIngest;

if (!REPO || !BANK) {
  console.error(
    "usage: node deepen.mjs --repo <path> [--bank <id>] [--harness <name>] " +
      "[--conversations f.json] [--api-url U] [--api-token X] [--config path] [--gitlog-limit N] [--git-ingest message|full|none]"
  );
  process.exit(1);
}

setLogLevel(cfg.logLevel);
// Foreground runs (benchmarks/acceptance) read stdout; background runs are followed via the leveled
// plugin log — every engine line goes to both.
const log = (m: string) => {
  console.log(`${new Date().toISOString()} ${m}`);
  plog.info("deepen", m);
};

/**
 * Past sessions arrive in one normalized interchange format (a ChatSession[] JSON export), so the
 * engine needs no per-host reader code — the host-specific locating/parsing lives with the hosts.
 */
async function readConversations(file: string | undefined): Promise<ChatSession[]> {
  if (!file) return [];
  return JSON.parse(readFileSync(file, "utf8")) as ChatSession[];
}

// ── per-bank lease: concurrent session starts must not double-ingest ─────────────
// Scratch, not state: in the OS temp dir so ~/.wenyuan holds ONLY the config file. A heartbeat
// keeps the lease live for however long the run takes; a dead run's lease goes stale in seconds.
// A fixed TTL would cut off longer runs and let the next session start double-ingest the bank —
// the same lease as the survey's.
const LEASE_ROOT = join(tmpdir(), "wenyuan-coding-agents", "deepen");
const lease = acquireLease(LEASE_ROOT, encodeURIComponent(FINAL_BANK ?? ""));
if (lease) {
  setInterval(() => {
    // Lost the lease (the machine slept past the stale window and another run took it): stop
    // here — deepen is resumable, the new holder finishes the work.
    if (!heartbeatLease(lease)) process.exit(0);
  }, LEASE_HEARTBEAT_MS).unref();
}

async function main() {
  if (!lease) {
    log(`deepen: another run holds the lease for ${FINAL_BANK} — nothing to do`);
    return;
  }
  const t0 = Date.now();
  diag("deepen", "deepen_started", { bank: FINAL_BANK });
  try {
    const client = new MemoryClient({
      apiUrl: API_URL,
      apiToken: API_TOKEN,
      bank: FINAL_BANK!,
      // Names the repository in every seeded page's query, so page synthesis can tell this
      // project's decisions from those of a dependency it merely discusses.
      //
      // A property of the BANK, not of this run's cwd: the query is PATCHed onto pages that
      // outlive the session, so a bank several repos share must not be told it is whichever one
      // ran last. `bankProjectName` answers only when the bank IS this repo's; a
      // `banks.<id>.bank` rename can point many repos at one destination, so a renamed bank is
      // not this repo's either. Undefined gives the shared-bank scope.
      project: PAGE_PROJECT,
      maxParallelRetains: cfg.maxParallelRetains,
      observationScopes: cfg.observationScopes,
      // Nobody waits on a background seed, and a 429 it gives up on is history missing from the
      // bank until some later session happens to re-run it.
      rateLimitPatienceMs: 10 * 60 * 1000,
      log,
    });
    log(`deepen -> ${client.apiUrl} bank=${FINAL_BANK} harness=${HARNESS}`);
    const stampFor = (sessionId?: string) =>
      buildRetainStamp(cfg, {
        directory: REPO!,
        harness: HARNESS,
        bankId: FINAL_BANK!,
        sessionId,
      });

    await client.configureBank({
      pageTrigger: buildPageTrigger(cfg),
      pages: cfg.pages,
      customPages: cfg.customPages,
      manage: cfg.manageBankConfig,
      extractionMode: cfg.retainExtractionMode,
      defaults: cfg.defaultBankConfig,
    });
    if (client.knowledgePagesSupported === false) {
      diag(HARNESS, "knowledge_pages_unavailable", {
        bank: FINAL_BANK,
        apiUrl: client.apiUrl,
      });
    }

    const gitIds = await client.listDocumentIds("source:git", "all_strict");

    // chats FIRST: few, and they carry the decisions that make memory necessary — never starved
    // behind the git flood. Dedup against what's already in the bank (chat:<id>).
    //
    // `retainSessions: false` covers THIS door too, not just the live write-back: history import
    // puts the very same conversations in the bank one session later, so honoring the flag in
    // only one of the two places would leave the opt-out cosmetic. Git ingest, knowledge pages
    // and bank configuration below are untouched by it.
    let sessions: ChatSession[] = [];
    if (!cfg.retainSessions) {
      log("[chat] retainSessions: false — skipping conversation import");
    } else {
      const chatIds = await client
        .listDocumentIds("source:chat", "all_strict")
        .catch(() => new Set<string>());
      const all = await readConversations(CONV);
      sessions = all.filter((s, i) => !chatIds.has(`chat:${s.id || `s${i}`}`));
      if (all.length !== sessions.length)
        log(
          `[chat] ${all.length - sessions.length} conversations already ingested — skipping those`
        );
    }
    const chatFails = await ingestChats(client, sessions, {
      concurrency: cfg.maxParallelRetains,
      log,
      stampFor,
    });

    // ── git: seeding and syncing are the SAME code — this idempotent pass runs every session,
    // so "keep the bank current" is just "run it again". cfg.gitIngest picks the depth:
    //   none    → git contributes nothing
    //   message → ONE aggregated commit-message doc, re-upserted when HEAD moves past the commit
    //             it was written at (same doc id, so it replaces — the gitlog-head:<sha> tag
    //             names that commit; see gitLogIsCurrent)
    //   full    → message doc + progressive per-commit full diffs, newest first (new commits land
    //             at the top of rev-list, so the next run ingests them: that IS the sync)
    let gitFails = 0;
    if (GIT_INGEST === "none") {
      log("[git] gitIngest=none — git ingestion disabled");
    } else {
      gitFails += await syncGitLog(client, REPO!, { limit: GITLOG_LIMIT, log, stampFor });

      if (GIT_INGEST === "full") {
        // progressive depth: next batch of un-ingested commits, newest first, full message + diff.
        try {
          const shas = execFileSync(
            "git",
            ["-C", REPO!, "rev-list", `-n`, String(DEEPEN_DIFF_TARGET), "HEAD"],
            // foreground runs inherit stdio: an empty repo's HEAD error must not print.
            { encoding: "utf8", windowsHide: true, stdio: ["ignore", "pipe", "pipe"] }
          )
            .trim()
            .split("\n")
            .filter(Boolean)
            .filter((sha) => !gitIds.has(`git:${sha}`))
            .slice(0, DIFF_BATCH);
          if (shas.length) {
            const repoName = repoNameOf(REPO!);
            log(`[deepen] ingesting ${shas.length} commits with full diffs (newest first) …`);
            await pool(
              shas,
              cfg.maxParallelRetains,
              (sha) => retainCommit(client, REPO!, sha, repoName, stampFor()),
              () => {
                gitFails++;
              }
            );
          } else {
            log(`[deepen] recent history fully deepened (target ${DEEPEN_DIFF_TARGET})`);
          }
        } catch {
          log("[deepen] no git history — skipping diff deepening");
        }
      }
    }

    // Survey-marker cosmetics: once the survey's findings exist, flip the newest reachable
    // baseline marker from "researching…" to "completed" (lazy — the detached survey agent can't
    // reliably do it itself). The `survey-state:done` tag makes this a one-time upsert.
    try {
      const uploads = await client
        .listDocumentIds("source:upload", "all_strict")
        .catch(() => new Set<string>());
      if (SURVEY_DOC_IDS.some((id) => uploads.has(id))) {
        const markers = await client.listDocumentIds("source:survey-baseline", "all_strict");
        const done = await client
          .listDocumentIds("survey-state:done", "all_strict")
          .catch(() => new Set<string>());
        let best: { id: string; sha: string; behind: number } | undefined;
        for (const id of markers) {
          if (done.has(id)) continue;
          const sha = id.replace(/^survey-baseline:/, "");
          const behind = commitsSince(REPO!, sha);
          if (behind !== null && (!best || behind < best.behind)) best = { id, sha, behind };
        }
        if (best) {
          const stamp = stampFor();
          const content =
            `✅ Codebase survey completed — baseline commit ${best.sha.slice(0, 12)}. ` +
            `(Internal marker: no memories are extracted from this document.)`;
          const tags = [...new Set([...stamp.tags, "source:survey-baseline", "survey-state:done"])];
          await client.retain(
            content,
            "wenyuan codebase-survey baseline",
            best.id,
            tags,
            "survey",
            {
              // `retain` only sets metadata when it is truthy, so an empty stamp sends none.
              metadata: Object.keys(stamp.metadata).length ? stamp.metadata : undefined,
            }
          );
          log(`[survey] marker ${best.id} flipped to completed`);
        }
      }
    } catch {
      /* cosmetics — best-effort */
    }

    await client.drain(client.opIds, "extraction");

    // The drain above only covers operations THIS run enqueued — consolidation and the template's
    // page refreshes run server-side on their own schedule. A settled bank requires ZERO active
    // ops, so wait (bounded) for the bank to fully settle before declaring the run complete.
    const settleDeadline = Date.now() + 15 * 60 * 1000;
    for (;;) {
      const active = await client.activeOperations().catch(() => 0);
      if (active === 0) break;
      if (Date.now() > settleDeadline) {
        log(`[deepen] ${active} server-side op(s) still active at settle timeout — proceeding`);
        break;
      }
      log(`[deepen] waiting for ${active} server-side op(s) to settle …`);
      await new Promise((r) => setTimeout(r, 5000));
    }

    const failures = chatFails + gitFails;
    diag("deepen", "deepen_done", {
      bank: FINAL_BANK,
      ms: Date.now() - t0,
      newChats: sessions.length,
      failures,
    });
    log(
      `\n✅ deepen complete in ${((Date.now() - t0) / 1000).toFixed(1)}s${failures ? ` (${failures} items failed to enqueue)` : ""}.`
    );
  } finally {
    releaseLease(lease);
  }
}

main().catch((e) => {
  diag("deepen", "deepen_failed", {
    bank: FINAL_BANK,
    error: describeError(e),
  });
  console.error("deepen failed:", (e as Error).message || e);
  if (lease) releaseLease(lease);
  process.exit(1);
});
