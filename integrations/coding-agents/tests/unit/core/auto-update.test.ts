import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  type AutoUpdateOptions,
  CHECK_INTERVAL_MS,
  isNewer,
  maybeAutoUpdate,
  npmViewVersion,
  PACKAGE_NAME,
  selfUpdatable,
  stagedVersion,
  stateFile,
} from "../../../src/core/auto-update";
import { log } from "../../../src/core/log";
import { DISABLE_HOOKS_ENV } from "../../../src/runtime/hook-contract";

describe("isNewer", () => {
  it("compares releases numerically, not lexically", () => {
    expect(isNewer("0.4.3", "0.4.2")).toBe(true);
    expect(isNewer("0.10.0", "0.9.9")).toBe(true); // the classic string-compare trap
    expect(isNewer("1.0.0", "0.99.99")).toBe(true);
    expect(isNewer("0.4.2", "0.4.3")).toBe(false);
    expect(isNewer("0.4.2", "0.4.2")).toBe(false);
  });

  it("never pulls a machine from a release onto a prerelease of it", () => {
    expect(isNewer("1.2.0-rc.1", "1.2.0")).toBe(false);
    expect(isNewer("1.2.0", "1.2.0-rc.1")).toBe(true);
    expect(isNewer("1.3.0-rc.1", "1.2.0")).toBe(true); // a later version still wins
  });

  it("refuses to act on a version it cannot parse", () => {
    expect(isNewer("latest", "0.4.2")).toBe(false);
    expect(isNewer("0.4", "0.4.2")).toBe(false);
    expect(isNewer("0.4.x", "0.4.2")).toBe(false);
    expect(isNewer("0.4.3", "")).toBe(false);
  });
});

describe("stagedVersion", () => {
  it("reads the version off the copy's own package.json, or empty when unreadable", () => {
    const dir = mkdtempSync(join(tmpdir(), "wenyuan-stagedver-"));
    try {
      writeFileSync(join(dir, "package.json"), JSON.stringify({ version: "1.2.3" }));
      expect(stagedVersion(dir)).toBe("1.2.3");
      writeFileSync(join(dir, "package.json"), "{not json");
      expect(stagedVersion(dir)).toBe("");
      writeFileSync(join(dir, "package.json"), JSON.stringify({ version: 42 }));
      expect(stagedVersion(dir)).toBe("");
      expect(stagedVersion(join(dir, "missing"))).toBe("");
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});

/**
 * The real lookup, exercised directly — only testing the fetch SEAM would let
 * the spawn protocol itself go unpinned. The contract under test: which binary,
 * which args, and how each way `npm view` can end maps to a version or "".
 */
describe("npmViewVersion", () => {
  const asRealSpawn = (m: ReturnType<typeof npmSpawn>): AutoUpdateOptions["spawn"] =>
    m as unknown as AutoUpdateOptions["spawn"];

  /** A spawn stub whose child feeds `out` on stdout, then closes with `code` — the events the
   *  implementation listens to. Emits on a microtask so listeners are registered first. */
  const npmSpawn = (code: number | null, out: string, error?: Error, stdoutError?: Error) =>
    vi.fn((_bin: string, _args: string[], _o: Record<string, unknown>) => {
      const listeners: Record<string, Array<(arg: unknown) => void>> = {};
      queueMicrotask(() => {
        for (const cb of listeners["stdout:data"] ?? []) cb(out);
        if (error) for (const cb of listeners["error"] ?? []) cb(error);
        if (stdoutError) for (const cb of listeners["stdout:error"] ?? []) cb(stdoutError);
        for (const cb of listeners["close"] ?? []) cb(code);
      });
      return {
        stdout: {
          setEncoding: (_e: string) => undefined,
          on: (ev: string, cb: (arg: unknown) => void) => {
            (listeners[`stdout:${ev}`] ??= []).push(cb);
          },
        },
        on: (ev: string, cb: (arg: unknown) => void) => {
          (listeners[ev] ??= []).push(cb);
        },
      };
    });

  it("asks npm for the version — the same resolver npx will install through", async () => {
    const spawn = npmSpawn(0, '"0.6.1"\n');
    expect(await npmViewVersion(PACKAGE_NAME, asRealSpawn(spawn))).toBe("0.6.1");
    const [bin, args, opts] = spawn.mock.calls[0]!;
    expect(bin).toBe("npm");
    // --json, so the version arrives as a quoted string on stdout rather than npm's human
    // output; a timeout bounds it (the NPM_VIEW_TIMEOUT_MS contract).
    expect(args).toEqual(["view", PACKAGE_NAME, "version", "--json"]);
    expect(opts.timeout).toBe(5000);
  });

  // On Windows npm is npm.cmd, which spawn cannot run without a shell ("spawn npm ENOENT").
  it("runs npm through a shell on Windows", async () => {
    const platform = Object.getOwnPropertyDescriptor(process, "platform")!;
    Object.defineProperty(process, "platform", { value: "win32" });
    try {
      const spawn = npmSpawn(0, '"0.6.1"\n');
      expect(await npmViewVersion("@x/y", asRealSpawn(spawn))).toBe("0.6.1");
      const [command, opts] = spawn.mock.calls[0] as unknown as [string, Record<string, unknown>];
      expect(command).toBe("npm view @x/y version --json");
      expect(opts).toMatchObject({ shell: true, windowsHide: true, timeout: 5000 });
    } finally {
      Object.defineProperty(process, "platform", platform);
    }
  });

  it("reads a failure as no-version, never as an exception", async () => {
    // non-zero exit (offline, auth failure), with npm's error event alongside…
    expect(await npmViewVersion("@x/y", asRealSpawn(npmSpawn(1, "", new Error("npm ERR!"))))).toBe(
      ""
    );
    // …a timeout kill arriving as close(null)…
    expect(await npmViewVersion("@x/y", asRealSpawn(npmSpawn(null, "")))).toBe("");
    // …unparseable stdout (an old npm printing something else)…
    expect(await npmViewVersion("@x/y", asRealSpawn(npmSpawn(0, "not json")))).toBe("");
    // …JSON, but not a single version string…
    expect(await npmViewVersion("@x/y", asRealSpawn(npmSpawn(0, '["0.6.1", "0.6.0"]')))).toBe("");
    // …a "version" carrying shell metacharacters — the whitelist is what stops it, not
    // isNewer: parseInt("3 && calc") is 3, so isNewer("1.2.3 && calc", "1.0.0") is true. The
    // value is headed for an argv entry now and a command line once a Windows shell wrapper
    // exists; a pre-release, by contrast, is still a release.
    expect(await npmViewVersion("@x/y", asRealSpawn(npmSpawn(0, '"1.2.3 && calc"')))).toBe("");
    expect(await npmViewVersion("@x/y", asRealSpawn(npmSpawn(0, '"1.2.3-rc.1"')))).toBe(
      "1.2.3-rc.1"
    );
    // …an error on the stdout pipe itself (EPIPE), which without a listener would be an
    // uncaught exception in the session-start hook process…
    expect(
      await npmViewVersion("@x/y", asRealSpawn(npmSpawn(0, "", undefined, new Error("EPIPE"))))
    ).toBe("");
  });

  it("force-settles when close never arrives — a grandchild holding the pipe would stall it", async () => {
    vi.useFakeTimers();
    try {
      // A child that never emits anything: no stdout, no error, no close. `close` waits for
      // the pipe to drain and would never fire, the spawn timeout only reaches the direct
      // child, and the update lock would sit held to LOCK_STALE_MS. The hard deadline is
      // the only way out.
      const kill = vi.fn();
      const spawn = vi.fn(() => ({
        stdout: { setEncoding: (_e: string) => undefined, on: () => undefined },
        on: () => undefined,
        kill,
      }));
      const p = npmViewVersion("@x/y", spawn as unknown as AutoUpdateOptions["spawn"]);
      // Genuinely pending before the deadline — not settled by spawn or any early event.
      expect(await Promise.race([p.then(() => true), Promise.resolve(false)])).toBe(false);
      await vi.advanceTimersByTimeAsync(10_000);
      await expect(p).resolves.toBe("");
      expect(kill).toHaveBeenCalledWith("SIGKILL");
    } finally {
      vi.useRealTimers();
    }
  });

  it("logs exactly one line per failed lookup — the trailing close event is not a second one", async () => {
    // Node reports a spawn failure as error FOLLOWED BY close(-2); the stub reproduces that
    // order. Only the count is asserted, not the wording — the contract is "one lookup, one
    // line", with the first reason winning.
    const logSpy = vi.spyOn(log, "info");
    try {
      await expect(
        npmViewVersion("@x/y", asRealSpawn(npmSpawn(-2, "", new Error("spawn npm ENOENT"))))
      ).resolves.toBe("");
      await expect(
        npmViewVersion("@x/y", asRealSpawn(npmSpawn(0, "", undefined, new Error("EPIPE"))))
      ).resolves.toBe("");
      // Inside the try: mockRestore() clears the call history, so asserting after the
      // finally would always see zero.
      expect(logSpy).toHaveBeenCalledTimes(2);
    } finally {
      logSpy.mockRestore();
    }
  });
});

describe("maybeAutoUpdate", () => {
  const dirs: string[] = [];
  const tmp = () => {
    const d = mkdtempSync(join(tmpdir(), "wenyuan-autoupdate-"));
    dirs.push(d);
    return d;
  };
  beforeEach(() => {
    // The refusal paths log; keep those lines inside the test's own scratch dir.
    process.env.WENYUAN_AGENTS_LOG_FILE = join(tmpdir(), "wenyuan-autoupdate-test.log");
    delete process.env.CLAUDE_PROJECT_DIR;
  });
  afterEach(() => {
    for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
    delete process.env.WENYUAN_AGENTS_LOG_FILE;
    delete process.env[DISABLE_HOOKS_ENV];
  });

  /** A spawn stub recording (bin, args, options) — the three things this module's contract is
   *  about — without standing up a real ChildProcess. */
  const spawnMock = () =>
    vi.fn((_bin: string, _args: string[], _o: Record<string, unknown>) => ({
      on: vi.fn(),
      unref: vi.fn(),
    }));
  const asSpawn = (m: ReturnType<typeof spawnMock>): AutoUpdateOptions["spawn"] =>
    m as unknown as AutoUpdateOptions["spawn"];

  /** A staged runtime directory holding `version`, plus the seams the checker needs. */
  const staged = (version: string) => {
    const runtime = tmp();
    writeFileSync(join(runtime, "package.json"), JSON.stringify({ version }));
    const spawn = spawnMock();
    // The seam answers with a version the way the real `npm view` does: it resolves "" for
    // failure, exactly like npmViewVersion does for a non-zero exit, a timeout kill, or no
    // npm to spawn. A stub that threw instead would test a path the real code cannot take.
    const npmViewOk = (latest: string) => vi.fn(async (_pkg: string) => latest);
    return { runtime, spawn, npmViewOk };
  };

  const opts = (
    runtime: string,
    extra: {
      spawn?: ReturnType<typeof spawnMock>;
      npmView?: (pkg: string) => Promise<string>;
      now?: number;
      lockFile?: string;
      selfUpdatable?: (dir: string) => boolean;
      binOnPath?: (bin: string) => boolean;
    }
  ): AutoUpdateOptions => ({
    pkgRoot: runtime,
    runtimeDir: runtime,
    // Per-test lock inside the throwaway runtime dir: the real one is machine-global, and
    // tests running in parallel would otherwise block each other for LOCK_STALE_MS.
    lockFile: extra.lockFile ?? join(runtime, "update.lock"),
    spawn: extra.spawn ? asSpawn(extra.spawn) : undefined,
    npmView: extra.npmView,
    now: extra.now,
    // Default the ownership guards open so each test exercises the behaviour it is about; the
    // guards themselves have their own tests below.
    selfUpdatable: extra.selfUpdatable ?? (() => true),
    binOnPath: extra.binOnPath ?? (() => true),
  });

  it("spawns a detached stage-only update when the registry is ahead", async () => {
    const { runtime, spawn, npmViewOk } = staged("0.4.2");
    const started = await maybeAutoUpdate(
      { autoUpdate: true },
      opts(runtime, { spawn, npmView: npmViewOk("0.4.3") })
    );

    expect(started).toBe("0.4.3");
    expect(spawn).toHaveBeenCalledTimes(1);
    const [bin, args, spawnOpts] = spawn.mock.calls[0]!;
    expect(bin).toBe("npx");
    // Pinned to the version we resolved, and `update` — never `install`, which would rewire hosts.
    expect(args).toEqual(["-y", `${PACKAGE_NAME}@0.4.3`, "update"]);
    expect(args).not.toContain("install");
    expect(spawnOpts.detached).toBe(true);
  });

  it("does nothing when the staged version is already current", async () => {
    const { runtime, spawn, npmViewOk } = staged("0.4.3");
    expect(
      await maybeAutoUpdate(
        { autoUpdate: true },
        opts(runtime, { spawn, npmView: npmViewOk("0.4.3") })
      )
    ).toBe("");
    expect(spawn).not.toHaveBeenCalled();
  });

  it("is off when autoUpdate is false — the flag reaches the registry call, not just the spawn", async () => {
    const { runtime, spawn, npmViewOk } = staged("0.4.2");
    const npmViewImpl = npmViewOk("0.4.3");
    expect(
      await maybeAutoUpdate({ autoUpdate: false }, opts(runtime, { spawn, npmView: npmViewImpl }))
    ).toBe("");
    expect(npmViewImpl).not.toHaveBeenCalled();
    expect(spawn).not.toHaveBeenCalled();
  });

  it("stands down under the hooks guard — a headless helper session must not race its parent", async () => {
    const { runtime, spawn, npmViewOk } = staged("0.4.2");
    const npmViewImpl = npmViewOk("0.4.3");
    process.env[DISABLE_HOOKS_ENV] = "1";
    expect(
      await maybeAutoUpdate({ autoUpdate: true }, opts(runtime, { spawn, npmView: npmViewImpl }))
    ).toBe("");
    expect(npmViewImpl).not.toHaveBeenCalled();
    expect(spawn).not.toHaveBeenCalled();
  });

  it("never touches a checkout or an npx run — only the staged copy updates itself", async () => {
    const { runtime, spawn, npmViewOk } = staged("0.4.2");
    const checkout = tmp();
    writeFileSync(join(checkout, "package.json"), JSON.stringify({ version: "0.4.2" }));
    expect(
      await maybeAutoUpdate(
        { autoUpdate: true },
        { ...opts(runtime, { spawn, npmView: npmViewOk("0.4.3") }), pkgRoot: checkout }
      )
    ).toBe("");
    expect(spawn).not.toHaveBeenCalled();
  });

  it("checks at most once per interval, and stamps the attempt even when it finds nothing", async () => {
    const { runtime, spawn, npmViewOk } = staged("0.4.2");
    const npmViewImpl = npmViewOk("0.4.2"); // up to date: nothing spawned, but the check still happened
    const t0 = 1_000_000_000;

    await maybeAutoUpdate(
      { autoUpdate: true },
      opts(runtime, { spawn, npmView: npmViewImpl, now: t0 })
    );
    expect(npmViewImpl).toHaveBeenCalledTimes(1);
    expect(JSON.parse(readFileSync(stateFile(runtime), "utf8"))).toMatchObject({ lastCheck: t0 });

    // A second session an hour later reads the stamp and does not call out again.
    await maybeAutoUpdate(
      { autoUpdate: true },
      opts(runtime, { spawn, npmView: npmViewImpl, now: t0 + 3_600_000 })
    );
    expect(npmViewImpl).toHaveBeenCalledTimes(1);

    // A day later it is due again.
    await maybeAutoUpdate(
      { autoUpdate: true },
      opts(runtime, { spawn, npmView: npmViewImpl, now: t0 + CHECK_INTERVAL_MS })
    );
    expect(npmViewImpl).toHaveBeenCalledTimes(2);
  });

  it("stamps a FAILED check too, so an offline machine asks once a day rather than every session", async () => {
    const { runtime, spawn } = staged("0.4.2");
    // Real lookup failures — non-zero exit, timeout kill, no npm to spawn — resolve "", the
    // same answer as "no newer version"; npmViewVersion never rejects, and the module's
    // never-throw guard is what keeps that equivalence safe.
    const offline = vi.fn(async (_pkg: string) => "");
    const t0 = 1_000_000_000;

    expect(
      await maybeAutoUpdate(
        { autoUpdate: true },
        opts(runtime, { spawn, npmView: offline, now: t0 })
      )
    ).toBe("");
    expect(JSON.parse(readFileSync(stateFile(runtime), "utf8"))).toMatchObject({ lastCheck: t0 });
    await maybeAutoUpdate(
      { autoUpdate: true },
      opts(runtime, { spawn, npmView: offline, now: t0 + 60_000 })
    );
    expect(offline).toHaveBeenCalledTimes(1);
    expect(spawn).not.toHaveBeenCalled();
  });

  // Several agents starting at once all read "due" before any has stamped it. Without a lock
  // they all spawn `update`, and two concurrent staging passes (rm, then recursive copy) can
  // leave a half-written runtime with missing entry points.
  it("lets only ONE of several simultaneous session starts spawn an updater", async () => {
    const { runtime, npmViewOk } = staged("0.4.2");
    const lock = join(tmp(), "auto-update.lock");
    const spawns = [spawnMock(), spawnMock(), spawnMock()];
    const now = 1_000_000_000;

    const started = await Promise.all(
      spawns.map((spawn) =>
        maybeAutoUpdate(
          { autoUpdate: true },
          opts(runtime, { spawn, npmView: npmViewOk("0.4.3"), now, lockFile: lock })
        )
      )
    );

    expect(started.filter(Boolean)).toEqual(["0.4.3"]);
    expect(spawns.filter((s) => s.mock.calls.length).length).toBe(1);
  });

  it("frees the lock when the check finds nothing, so the next window is not blocked", async () => {
    const { runtime, spawn, npmViewOk } = staged("0.4.2");
    const lock = join(tmp(), "auto-update.lock");
    const t0 = 1_000_000_000;

    await maybeAutoUpdate(
      { autoUpdate: true },
      opts(runtime, { spawn, npmView: npmViewOk("0.4.2"), now: t0, lockFile: lock })
    );
    expect(existsSync(lock)).toBe(false);

    // A day later the next check can claim it and act.
    expect(
      await maybeAutoUpdate(
        { autoUpdate: true },
        opts(runtime, {
          spawn,
          npmView: npmViewOk("0.4.3"),
          now: t0 + CHECK_INTERVAL_MS,
          lockFile: lock,
        })
      )
    ).toBe("0.4.3");
  });

  it("treats a lock whose holder is gone as stale rather than waiting out the TTL", async () => {
    const { runtime, spawn, npmViewOk } = staged("0.4.2");
    const lock = join(tmp(), "auto-update.lock");
    // pid 2^22 is above every Linux/macOS pid_max — nothing can be running under it.
    writeFileSync(lock, JSON.stringify({ pid: 4_194_304, ts: Date.now() }));

    expect(
      await maybeAutoUpdate(
        { autoUpdate: true },
        opts(runtime, { spawn, npmView: npmViewOk("0.4.3"), lockFile: lock })
      )
    ).toBe("0.4.3");
  });

  // An operator who installs globally (or vendors the package as a project dependency)
  // manages its version with their package manager. Re-staging behind their back would leave
  // the package manager naming a version that is no longer what runs, with no way to
  // reconcile the two.
  it("leaves a runtime alone when it was not staged from an npx download", async () => {
    const { runtime, spawn, npmViewOk } = staged("0.4.2");
    const npmViewImpl = npmViewOk("0.4.3");
    expect(
      await maybeAutoUpdate(
        { autoUpdate: true },
        opts(runtime, { spawn, npmView: npmViewImpl, selfUpdatable: () => false })
      )
    ).toBe("");
    expect(npmViewImpl).not.toHaveBeenCalled();
    expect(spawn).not.toHaveBeenCalled();
  });

  it("skips the check entirely when npx is not on PATH — there would be nothing to spawn", async () => {
    const { runtime, spawn, npmViewOk } = staged("0.4.2");
    const npmViewImpl = npmViewOk("0.4.3");
    expect(
      await maybeAutoUpdate(
        { autoUpdate: true },
        opts(runtime, { spawn, npmView: npmViewImpl, binOnPath: (bin) => bin !== "npx" })
      )
    ).toBe("");
    expect(npmViewImpl).not.toHaveBeenCalled();
    expect(spawn).not.toHaveBeenCalled();
  });

  it("skips the check entirely when npm is not on PATH — there would be no version to look up", async () => {
    const { runtime, spawn, npmViewOk } = staged("0.4.2");
    const npmViewImpl = npmViewOk("0.4.3");
    expect(
      await maybeAutoUpdate(
        { autoUpdate: true },
        opts(runtime, { spawn, npmView: npmViewImpl, binOnPath: (bin) => bin !== "npm" })
      )
    ).toBe("");
    expect(npmViewImpl).not.toHaveBeenCalled();
    expect(spawn).not.toHaveBeenCalled();
  });

  it("stamps both refusals, so neither repeats its reason on every session start", async () => {
    const { runtime, spawn, npmViewOk } = staged("0.4.2");
    const t0 = 1_000_000_000;
    await maybeAutoUpdate(
      { autoUpdate: true },
      opts(runtime, { spawn, npmView: npmViewOk("0.4.3"), now: t0, selfUpdatable: () => false })
    );
    expect(JSON.parse(readFileSync(stateFile(runtime), "utf8"))).toMatchObject({ lastCheck: t0 });
  });

  it("never guesses when the staged version is unreadable", async () => {
    const runtime = tmp(); // no package.json at all
    const spawn = spawnMock();
    const npmViewImpl = vi.fn(async () => "9.9.9");
    expect(
      await maybeAutoUpdate({ autoUpdate: true }, opts(runtime, { spawn, npmView: npmViewImpl }))
    ).toBe("");
    expect(npmViewImpl).not.toHaveBeenCalled();
    expect(spawn).not.toHaveBeenCalled();
  });

  it("survives a spawn that fails asynchronously", async () => {
    const { runtime, npmViewOk } = staged("0.4.2");
    const handlers: Record<string, (e: Error) => void> = {};
    const spawn = vi.fn((_bin: string, _args: string[], _o: Record<string, unknown>) => ({
      on: (event: string, cb: (e: Error) => void) => {
        handlers[event] = cb;
      },
      unref: vi.fn(),
    }));
    await maybeAutoUpdate(
      { autoUpdate: true },
      {
        pkgRoot: runtime,
        runtimeDir: runtime,
        spawn: asSpawn(spawn as never),
        npmView: npmViewOk("0.4.3"),
      }
    );
    expect(() => handlers.error?.(new Error("spawn npx ENOENT"))).not.toThrow();
  });
});

/**
 * The origin marker is what separates "we downloaded this" from "somebody else's copy". It
 * fails closed on purpose: a wrong `true` overwrites a global install or a developer's built
 * output, while a wrong `false` costs one manual install.
 */
describe("selfUpdatable", () => {
  const dirs: string[] = [];
  const tmp = () => {
    const d = mkdtempSync(join(tmpdir(), "wenyuan-origin-"));
    dirs.push(d);
    return d;
  };
  afterEach(() => {
    for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
  });

  const withOrigin = (source: unknown): string => {
    const d = tmp();
    writeFileSync(join(d, ".install-origin.json"), JSON.stringify({ source }));
    return d;
  };

  it("accepts a runtime staged from an npx cache", () => {
    expect(
      selfUpdatable(withOrigin("/Users/u/.npm/_npx/a1b2c3/node_modules/wenyuan-coding-agents"))
    ).toBe(true);
    // Windows separators reach the same verdict — the marker records whatever path staged it.
    expect(
      selfUpdatable(withOrigin("C:\\Users\\u\\AppData\\npm-cache\\_npx\\a1\\node_modules\\pkg"))
    ).toBe(true);
  });

  it("refuses a global install, a project dependency and a checkout", () => {
    expect(selfUpdatable(withOrigin("/usr/local/lib/node_modules/wenyuan-coding-agents"))).toBe(
      false
    );
    expect(selfUpdatable(withOrigin("/home/u/proj/node_modules/wenyuan-coding-agents"))).toBe(
      false
    );
    expect(selfUpdatable(withOrigin("/home/u/dev/wenyuan/integrations/coding-agents"))).toBe(false);
  });

  it("refuses a runtime with no marker, an unreadable one, or an empty source", () => {
    expect(selfUpdatable(tmp())).toBe(false);
    const broken = tmp();
    writeFileSync(join(broken, ".install-origin.json"), "not json");
    expect(selfUpdatable(broken)).toBe(false);
    expect(selfUpdatable(withOrigin(""))).toBe(false);
    expect(selfUpdatable(withOrigin(42))).toBe(false);
  });
});
