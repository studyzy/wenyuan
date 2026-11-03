/**
 * Keep the STAGED runtime current on its own.
 *
 * `install` copies the built runtime into ~/.wenyuan/wenyuan-memory and points
 * every wired host's hooks at that copy. Nothing refreshed it afterwards: the
 * only update path was the operator remembering to re-run install, so a machine
 * could sit several versions behind indefinitely — bugs stayed fixed only for
 * people who happened to re-install.
 *
 * Once per CHECK_INTERVAL_MS, at session start, this asks npm for the published
 * version and — when it is newer than the staged one — spawns a DETACHED
 * `npx wenyuan-coding-agents@<version> update`, which re-stages the runtime and
 * touches no host config. Fire-and-forget: the current session keeps running
 * the version it already loaded and the next one starts on the new code.
 *
 * Deliberately narrow:
 *   - it runs ONLY from the staged copy. A checkout or an npx run is somebody's
 *     development or one-off invocation, and overwriting it with a published
 *     build would destroy their work.
 *   - it replaces ONLY a runtime it can prove was staged from an npx download
 *     (selfUpdatable below). A copy staged from a global install, a project
 *     dependency, or a local checkout belongs to whoever manages that source.
 *   - it needs npx and npm on PATH — npx is how the updater is fetched, npm is
 *     how the version is looked up. Without them it says so once a day rather
 *     than failing a spawn each time.
 *   - it stages only. Rewiring hosts unattended would mean choosing which
 *     agents to install for, and that is the operator's call.
 *   - autoUpdate: false in the stored configuration (or the hooks guard
 *     variable) turns it off entirely, for pinned or air-gapped setups.
 *
 * Known window: staging replaces the runtime directory wholesale, so a hook
 * that happens to spawn during that copy can fail to load its entry point.
 * Already-running processes are unaffected, the window is milliseconds once a
 * day, and the cost of losing it is one turn without memory. Serialising
 * against every possible concurrent hook spawn would need a lock every hook
 * takes on every turn — a worse trade than the window it closes.
 */
import { type ChildProcess, type SpawnOptions, spawn as realSpawn } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  readFileSync,
  realpathSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { hooksDisabled } from "../runtime/hook-contract";
import { homeDirectory, PER_USER_DIRECTORY_NAME, RUNTIME_DIRECTORY_NAME } from "../runtime/layout";
import type { Config } from "./config/index";
import { describeError, log } from "./log";
import { binOnPath } from "./util";

export const PACKAGE_NAME = "wenyuan-coding-agents";

/** A version `npm view` may return: digits.digits.digits with an optional pre-release. */
const RELEASE_RE = /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?$/;

/** How often the registry is asked. One session a day pays the npm CLI startup
 * behind `npm view`; the rest read a timestamp off disk and move on. */
export const CHECK_INTERVAL_MS = 24 * 60 * 60 * 1000;

/** Version-check budget. A slow or unreachable registry must not delay a session
 * start, and this runs before the operator has typed anything. */
const NPM_VIEW_TIMEOUT_MS = 5000;

/** Hard wall past the spawn timeout: `close` waits for stdio to drain, so a
 * grandchild holding the pipe could stall the lookup after npm itself is killed. */
const NPM_VIEW_HARD_DEADLINE_MS = NPM_VIEW_TIMEOUT_MS + 1000;

/** Where the last check's timestamp lives — inside the staged runtime, so it is
 * removed with it. */
export function stateFile(runtimeDir: string): string {
  return join(runtimeDir, ".update-check.json");
}

/** The directory this module is running out of (the package root of the copy in
 * use). In the staged runtime the bundled hook sits one directory below the
 * runtime root, so this names the staged copy; from a checkout it names a
 * source directory, which the ownership gate then refuses. */
function packageRoot(): string {
  return join(dirname(fileURLToPath(import.meta.url)), "..");
}

/** Where install stages the runtime — the one copy this may replace. Kept as
 * layout constants rather than a call into the installer, which would pull the
 * whole install path into every hook bundle. */
function stagedRuntimeDir(): string {
  return join(homeDirectory(), PER_USER_DIRECTORY_NAME, RUNTIME_DIRECTORY_NAME);
}

/** Same directory, compared through realpath — a symlinked or differently-spelled
 * HOME must not read as "not the staged copy" and silently disable updates. */
function sameDir(a: string, b: string): boolean {
  try {
    return realpathSync(a) === realpathSync(b);
  } catch {
    return a === b;
  }
}

/** Version of the package this code belongs to, or "" when it cannot be read. */
export function stagedVersion(pkgRoot: string): string {
  try {
    const pkg = JSON.parse(readFileSync(join(pkgRoot, "package.json"), "utf8")) as {
      version?: string;
    };
    return typeof pkg.version === "string" ? pkg.version : "";
  } catch {
    return "";
  }
}

/**
 * Is `candidate` a later release than `current`?
 *
 * A deliberately small comparison rather than a semver dependency: this package
 * ships zero-dependency, and the only question asked here is "did the release
 * number go up". NOT util.ts's semverGte, which answers a different question for
 * capability probes: it is true on equality and strips pre-release suffixes, so
 * it would both re-update a machine that is already current and drag a release
 * onto its own release candidate. A pre-release suffix loses to the same
 * numbers without one, which is what keeps a machine on 1.2.0 from being pulled
 * onto 1.2.0-rc.1; two pre-releases of the same version compare as equal.
 */
export function isNewer(candidate: string, current: string): boolean {
  const parse = (v: string): { nums: number[]; pre: boolean } => {
    const [core = "", ...rest] = v.trim().split("-");
    return {
      nums: core.split(".").map((n) => Number.parseInt(n, 10)),
      pre: rest.length > 0,
    };
  };
  const a = parse(candidate);
  const b = parse(current);
  if (a.nums.length !== 3 || b.nums.length !== 3) return false;
  if (a.nums.some((n) => Number.isNaN(n)) || b.nums.some((n) => Number.isNaN(n))) return false;
  for (let i = 0; i < 3; i++) {
    const ai = a.nums[i];
    const bi = b.nums[i];
    if (ai === undefined || bi === undefined) return false;
    if (ai !== bi) return ai > bi;
  }
  // Same numbers: only a release can supersede a pre-release of itself.
  return b.pre && !a.pre;
}

/** Whether enough time has passed since the last check. An unreadable or absent
 * state file reads as "never checked", so a first run always checks and a
 * corrupted one self-heals. */
function dueForCheck(file: string, now: number): boolean {
  try {
    const state = JSON.parse(readFileSync(file, "utf8")) as { lastCheck?: number };
    return typeof state.lastCheck !== "number" || now - state.lastCheck >= CHECK_INTERVAL_MS;
  } catch {
    return true;
  }
}

/** Written at staging time, naming the directory this runtime was copied from. */
const ORIGIN_FILE = ".install-origin.json";

/**
 * May this runtime be replaced automatically?
 *
 * Only when it was staged from an npx download — the documented install path,
 * where no other tool is tracking the version. A global install and a project
 * dependency are managed by npm (an unattended re-stage would leave `npm ls -g`
 * naming a version that is no longer what runs), and a checkout is a
 * developer's build, which a published release would silently overwrite.
 *
 * Missing marker = no. It is written on every install/update from the version
 * that introduced auto-update onward, and a machine has to re-install once to
 * get this code at all. Failing closed here costs one manual install; failing
 * open costs somebody their working tree.
 */
export function selfUpdatable(runtimeDir: string): boolean {
  try {
    const origin = JSON.parse(readFileSync(join(runtimeDir, ORIGIN_FILE), "utf8")) as {
      source?: string;
    };
    if (typeof origin.source !== "string" || !origin.source) return false;
    // npx unpacks into a _npx/<hash>/node_modules/... cache directory; nothing else does.
    return origin.source.split(/[\\/]/).includes("_npx");
  } catch {
    return false; // absent or unreadable — this fails closed on purpose
  }
}

/**
 * Serialise updaters across concurrent sessions.
 *
 * The 24h stamp is not enough on its own: several agents starting within the
 * same second all read "due" before any of them has written it, so they all
 * spawn an updater. Two update runs racing means two staging passes, and
 * staging is rm then recursive copy — one process can delete the directory the
 * other is half way through writing, leaving a runtime with missing entry
 * points. This is a plugin for machines that routinely run several agents at
 * once, so the burst is not hypothetical.
 *
 * A TTL alone would wedge the updater for its whole window after a crash, so
 * the holder's pid decides liveness. The pid stored is the detached child's,
 * because the copy happens in the child and outlives this process. The lock
 * lives in the OS temp dir on purpose: it is scratch, and a reboot clearing it
 * can only cost one redundant check.
 */
const LOCK_STALE_MS = 10 * 60 * 1000;

function lockFile(): string {
  return join(tmpdir(), PACKAGE_NAME, "auto-update.lock");
}

function acquireUpdateLock(file: string, now: number): boolean {
  try {
    const held = JSON.parse(readFileSync(file, "utf8")) as { pid?: number; ts?: number };
    if (held.ts && now - held.ts < LOCK_STALE_MS) {
      let holderAlive = false;
      if (held.pid) {
        try {
          process.kill(held.pid, 0);
          holderAlive = true;
        } catch {
          /* ESRCH: the holder died — the lock is stale NOW, not in LOCK_STALE_MS */
        }
      }
      if (holderAlive) return false;
    }
  } catch {
    /* no or unreadable lock — free */
  }
  try {
    mkdirSync(dirname(file), { recursive: true });
    writeFileSync(file, JSON.stringify({ pid: process.pid, ts: now }));
    return true;
  } catch {
    // Cannot claim the lock, so we cannot prove we are the only updater. Skip rather than race.
    return false;
  }
}

/** Hand the lock to the detached child, whose staging is what actually needs guarding. */
function holdLockFor(file: string, pid: number | undefined, now: number): void {
  try {
    if (pid === undefined) return releaseUpdateLock(file);
    writeFileSync(file, JSON.stringify({ pid, ts: now }));
  } catch {
    /* best-effort */
  }
}

function releaseUpdateLock(file: string): void {
  try {
    unlinkSync(file);
  } catch {
    /* already gone */
  }
}

/**
 * Stamp the check BEFORE acting on its result.
 *
 * The spawned update can fail — offline, a registry hiccup, a read-only home —
 * and re-checking on every session start until it succeeds would turn one
 * broken machine into a request per session. Recording the attempt bounds the
 * retry to once per interval whatever the outcome.
 */
function stampCheck(file: string, now: number, latest: string): void {
  try {
    writeFileSync(file, JSON.stringify({ lastCheck: now, latest }));
  } catch {
    /* best-effort: an unwritable state file means we re-check next session, nothing worse */
  }
}

/**
 * Spawn npm or npx. On Windows both are .cmd shims, which spawn only runs
 * through a shell — without one it fails with ENOENT and the update check
 * never ran there. The command goes as one string because Node deprecates
 * passing args together with shell; every word is a fixed literal, the package
 * name or a RELEASE_RE-checked version, so none needs quoting.
 */
function spawnNpm(
  spawnImpl: typeof realSpawn,
  bin: "npm" | "npx",
  args: string[],
  opts: SpawnOptions
): ChildProcess {
  return process.platform === "win32"
    ? spawnImpl([bin, ...args].join(" "), { ...opts, shell: true })
    : spawnImpl(bin, args, opts);
}

/** Ask npm for the published version, or "" if it cannot be determined.
 *
 * `npm view`, not a fetch of the registry host: the update this triggers is
 * `npx <pkg>@<v>`, which resolves through the operator's npm config (mirror,
 * proxy, private registry). A hardcoded fetch disagreed with it — on a mirrored
 * machine it timed out while npx worked, so auto-update silently never fired.
 * Runs in the caller's cwd on purpose, like the npx spawn, so both read the
 * same project .npmrc.
 *
 * Every failure resolves "" ("no newer version") and logs exactly one info line
 * with the reason — without it a failed check is indistinguishable from "already
 * current". The reason rides done() so the settled guard also dedupes the log
 * (a spawn failure is error THEN close).
 */
export async function npmViewVersion(
  pkg: string,
  spawnImpl: typeof realSpawn = realSpawn
): Promise<string> {
  return new Promise((resolve) => {
    let out = "";
    let settled = false;
    let deadline: ReturnType<typeof setTimeout> | undefined;
    const done = (v: string, reason?: string) => {
      if (settled) return;
      settled = true;
      if (deadline) clearTimeout(deadline);
      if (reason) log.info("auto-update", `version check failed: ${reason}`);
      resolve(v);
    };
    try {
      const child = spawnNpm(spawnImpl, "npm", ["view", pkg, "version", "--json"], {
        timeout: NPM_VIEW_TIMEOUT_MS,
        stdio: ["ignore", "pipe", "ignore"],
        windowsHide: true,
      });
      child.stdout?.setEncoding("utf8");
      child.stdout?.on("data", (d: string) => {
        out += d;
      });
      // An unhandled pipe 'error' would crash the session-start hook.
      child.stdout?.on("error", (e) => done("", describeError(e)));
      // ENOENT / EACCES: npm is not where PATH said it was.
      child.on("error", (e) => done("", describeError(e)));
      // close, not exit: stdout must have fully arrived before the parse.
      child.on("close", (code, signal) => {
        if (code !== 0) return done("", `npm view exited ${code ?? signal}`);
        try {
          const v = JSON.parse(out.trim()) as unknown;
          // --json on one field is a quoted string; anything else is not a version to pin.
          if (typeof v !== "string")
            return done("", `unparsable npm output: ${out.trim().slice(0, 80)}`);
          // Whitelist: the value reaches a spawn argv, and isNewer("1.2.3 && calc", ...) is true.
          if (!RELEASE_RE.test(v)) return done("", `not a release: ${v.slice(0, 40)}`);
          done(v);
        } catch {
          done("", `unparsable npm output: ${out.trim().slice(0, 80)}`);
        }
      });
      // Backstop for a stalled pipe past the kill. Not unref'd: it must keep the
      // process alive long enough to settle and release the update lock.
      deadline = setTimeout(() => {
        try {
          child.kill("SIGKILL");
        } catch {
          /* already dead */
        }
        done("", "npm view never exited — hard deadline");
      }, NPM_VIEW_HARD_DEADLINE_MS);
    } catch (e) {
      done("", describeError(e));
    }
  });
}

export interface AutoUpdateOptions {
  /** Package root to treat as "where this code runs from" (tests). */
  pkgRoot?: string;
  /** The staged runtime directory this may update (tests); defaults to the per-user runtime. */
  runtimeDir?: string;
  /** Cross-process updater lock (tests); defaults to one in the OS temp dir. */
  lockFile?: string;
  /** Seams for the two ownership guards (tests). */
  selfUpdatable?: (runtimeDir: string) => boolean;
  binOnPath?: (bin: string) => boolean;
  spawn?: typeof realSpawn;
  /** Version lookup seam (tests); defaults to the real `npm view`. */
  npmView?: (pkg: string) => Promise<string>;
  now?: number;
}

/**
 * Check for a newer release and, if there is one, spawn the detached updater.
 * Awaitable so tests (and callers that want to) can observe it; production call
 * sites fire and forget. Never throws.
 *
 * Returns the version an update was started for, or "" when nothing was done.
 */
export async function maybeAutoUpdate(
  cfg: Pick<Config, "autoUpdate">,
  opts: AutoUpdateOptions = {}
): Promise<string> {
  try {
    if (!cfg.autoUpdate) return "";
    // A headless helper session (the survey's own runs) must not race the
    // runtime out from under its parent, so the hooks guard silences this too.
    if (hooksDisabled()) return "";

    const pkgRoot = opts.pkgRoot ?? packageRoot();
    const runtime = opts.runtimeDir ?? stagedRuntimeDir();
    if (!existsSync(runtime)) return "";
    // Only the staged copy updates itself — see the module doc.
    if (!sameDir(pkgRoot, runtime)) return "";

    const now = opts.now ?? Date.now();
    const file = stateFile(runtime);
    if (!dueForCheck(file, now)) return "";

    const current = stagedVersion(pkgRoot);
    if (!current) return ""; // cannot tell what is installed — never guess and overwrite it
    // Both guards sit after the interval gate and stamp like any other "checked,
    // nothing to do" outcome, so each states its reason at most once a day
    // instead of on every session start.
    if (!(opts.selfUpdatable ?? selfUpdatable)(runtime)) {
      log.info("auto-update", "runtime is managed outside npx — leaving its version alone");
      stampCheck(file, now, "");
      return "";
    }
    // Probed BEFORE the registry call: with no npx there is nothing to spawn,
    // and with no npm there is no version to look up. npx and npm ship
    // together, so this is usually one verdict.
    const binOk = opts.binOnPath ?? binOnPath;
    if (!binOk("npx") || !binOk("npm")) {
      log.info("auto-update", "npx/npm is not on PATH — skipping the update check");
      stampCheck(file, now, "");
      return "";
    }

    // Claimed before the registry call, so a burst of simultaneous session
    // starts makes ONE request and can only ever produce one updater.
    const lock = opts.lockFile ?? lockFile();
    if (!acquireUpdateLock(lock, now)) return "";
    try {
      const latest = await (opts.npmView ?? npmViewVersion)(PACKAGE_NAME);
      stampCheck(file, now, latest);
      if (!latest || !isNewer(latest, current)) {
        releaseUpdateLock(lock);
        return "";
      }

      log.info("auto-update", `updating the WenYuan runtime ${current} -> ${latest}`);
      const child = spawnNpm(
        opts.spawn ?? realSpawn,
        "npx",
        ["-y", `${PACKAGE_NAME}@${latest}`, "update"],
        {
          detached: true,
          stdio: "ignore",
          windowsHide: true,
        }
      );
      // A spawn failure (no npx on PATH, EACCES) arrives asynchronously as an
      // 'error' event; an unhandled one would take the session start down with it.
      child.on("error", (e) => {
        log.warn("auto-update", `update spawn failed: ${e.message}`);
        releaseUpdateLock(lock);
      });
      child.unref();
      holdLockFor(lock, child.pid, now);
      return latest;
    } catch (e) {
      releaseUpdateLock(lock);
      throw e;
    }
  } catch {
    return ""; // an update check must never break a session
  }
}
