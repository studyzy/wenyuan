/**
 * The detached survey supervisor, as a REAL process: it starts the agent from the spec it
 * was handed, strips the spec from the agent's environment, releases the lease when the
 * agent exits — and a SIGTERM tears both down. The gated end-to-end suite then drives the
 * real startCodebaseSurvey through six racing hooks: exactly one survey admitted, the
 * supervisor outliving its hook, and a dead supervisor's lease reclaimed by one winner.
 *
 * The gated suite needs the built dist/runtime/mcp-server.mjs (the inline MCP config names
 * it), so it runs only under WENYUAN_AGENTS_SUPERVISOR_E2E=1 — "npm run build" first.
 */
import { execFile, spawn, type ChildProcess } from "node:child_process";
import { mkdtempSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { promisify } from "node:util";
import { buildSync } from "esbuild";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { acquireLease, SURVEY_SPEC_ENV, type SurveyLease } from "../../../src/core/survey-lease";

const here = dirname(fileURLToPath(import.meta.url));
const execFileAsync = promisify(execFile);
const E2E = process.env.WENYUAN_AGENTS_SUPERVISOR_E2E === "1";

let buildDir: string;
let supervisor: string;
beforeAll(() => {
  buildDir = mkdtempSync(join(tmpdir(), "supervisor-build-"));
  supervisor = join(buildDir, "survey-supervisor.mjs");
  buildSync({
    entryPoints: [join(here, "../../../src/core/survey-supervisor.ts")],
    bundle: true,
    platform: "node",
    format: "esm",
    outfile: supervisor,
    logLevel: "silent",
  });
});
afterAll(() => rmSync(buildDir, { recursive: true, force: true }));

/** A stand-in survey agent: records its pid, idles like a long survey, and would leave a
 *  marker behind if the spec were still in its environment. */
function agentSource(scratch: string): string {
  return (
    `const fs = require("node:fs");` +
    `if (process.env.${SURVEY_SPEC_ENV}) fs.writeFileSync(${JSON.stringify(scratch)} + "/leaked-spec", "");` +
    `fs.writeFileSync(${JSON.stringify(scratch)} + "/pid-" + process.pid, "");` +
    `setInterval(() => {}, 1000);`
  );
}

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

const agentPids = (scratch: string) =>
  readdirSync(scratch)
    .filter((name) => name.startsWith("pid-"))
    .map((name) => Number(name.slice(4)));

async function waitFor(cond: () => boolean, what: string): Promise<void> {
  for (let i = 0; i < 100; i++) {
    if (cond()) return;
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(what);
}

describe("survey-supervisor (real process)", () => {
  let scratch: string;
  let root: string;
  let lease: SurveyLease;
  const children: ChildProcess[] = [];

  beforeAll(() => {
    scratch = mkdtempSync(join(tmpdir(), "supervisor-run-"));
    root = mkdtempSync(join(tmpdir(), "supervisor-lease-"));
  });

  it("refuses to run without a spec", async () => {
    const child = spawn(process.execPath, [supervisor], { stdio: "ignore" });
    children.push(child);
    expect(await new Promise<number | null>((r) => child.once("exit", (code) => r(code)))).not.toBe(
      0
    );
  });

  it("runs the agent with the spec stripped, and SIGTERM tears both down and releases", async () => {
    lease = acquireLease(root, "k")!;
    const child = spawn(process.execPath, [supervisor], {
      env: {
        ...process.env,
        [SURVEY_SPEC_ENV]: JSON.stringify({
          lease,
          bin: process.execPath,
          args: ["-e", agentSource(scratch)],
          heartbeatMs: 200,
        }),
      },
      stdio: ["ignore", "ignore", "pipe"],
    });
    children.push(child);
    let stderr = "";
    child.stderr?.on("data", (d) => (stderr += d));

    await waitFor(() => agentPids(scratch).length === 1, "the supervisor never started the agent");
    // The spec is deleted before the agent starts — it must not travel further.
    expect(readdirSync(scratch)).not.toContain("leaked-spec");
    // The lease stays held for the agent's lifetime.
    expect(readdirSync(lease.directory)).toEqual([lease.owner]);

    child.kill("SIGTERM");
    const code = await new Promise<number | null>((r) => child.once("exit", (c) => r(c)));
    expect(code).not.toBeNull();
    await waitFor(() => !alive(agentPids(scratch)[0]!), "the agent outlived the supervisor");
    await waitFor(() => readdirSync(root).length === 0, "the lease was not released");
    if (stderr) throw new Error(`supervisor failed: ${stderr}`);
  });

  afterAll(() => {
    for (const c of children) c.kill("SIGKILL");
    rmSync(scratch, { recursive: true, force: true });
    rmSync(root, { recursive: true, force: true });
  });
});

describe.skipIf(!E2E)("survey single-flight end to end", () => {
  let lockDir: string;
  let survey: string;

  beforeAll(() => {
    lockDir = mkdtempSync(join(tmpdir(), "supervisor-e2e-"));
    survey = join(lockDir, "survey.mjs");
    // The createRequire banner keeps any CJS-shaped dependency resolvable from the ESM bundle.
    const banner = {
      js: 'import { createRequire } from "node:module"; const require = createRequire(import.meta.url);',
    };
    buildSync({
      entryPoints: [join(here, "../../../src/core/survey.ts")],
      bundle: true,
      platform: "node",
      format: "esm",
      outfile: survey,
      banner,
      logLevel: "silent",
    });
    writeFileSync(
      join(lockDir, "config.json"),
      JSON.stringify({ bankId: "bank-1", apiUrl: "https://api.example.test", apiToken: "t" })
    );
  });

  const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

  it("single-flights independent hooks, survives their exit, and recovers a dead supervisor", async () => {
    const staleMs = 2_000;
    // The hook: launch the survey the way a session-start hook would, with the real
    // supervisor — only the agent itself is swapped for the stand-in.
    const hook = `
      import { startCodebaseSurvey } from ${JSON.stringify(pathToFileURL(survey).href)};
      import { spawn } from 'node:child_process';
      const started = await startCodebaseSurvey(${JSON.stringify(lockDir)}, {
        exists: () => true,
        mcpServerPath: ${JSON.stringify(join(here, "../../../dist/runtime/mcp-server.mjs"))},
        supervisorPath: ${JSON.stringify(supervisor)},
        lease: { dir: ${JSON.stringify(join(lockDir, "locks"))}, staleMs: ${staleMs}, heartbeatMs: 200 },
        spawn: (node, argv, options) => {
          const spec = { ...JSON.parse(options.env.${SURVEY_SPEC_ENV}), bin: process.execPath, args: ['-e', ${JSON.stringify(agentSource(lockDir))}] };
          return spawn(node, argv, { ...options, env: { ...options.env, [${JSON.stringify(SURVEY_SPEC_ENV)}]: JSON.stringify(spec) } });
        },
      });
      console.log(JSON.stringify(started));
    `;
    const runHook = async () => {
      const { stdout } = await execFileAsync(
        process.execPath,
        ["--input-type=module", "-e", hook],
        {
          env: {
            PATH: process.env.PATH,
            HOME: lockDir,
            WENYUAN_AGENTS_CONFIG: join(lockDir, "config.json"),
            SystemRoot: process.env.SystemRoot,
          },
        }
      );
      return JSON.parse(stdout.trim()) as boolean;
    };
    const race = async () =>
      (await Promise.all(Array.from({ length: 6 }, runHook))).filter(Boolean).length;
    const agents = () =>
      readdirSync(lockDir)
        .filter((name) => name.startsWith("pid-"))
        .map((name) => {
          const [, pid, ppid] = name.split("-").map(Number);
          return { pid: pid ?? 0, ppid: ppid ?? 0 };
        });
    try {
      expect(await race()).toBe(1);
      await waitFor(() => agents().length === 1, "no agent ever started");
      // The supervisor strips the spec before starting the agent.
      expect(readdirSync(lockDir)).not.toContain("leaked-spec");
      // Every hook has exited; past the stale window the heartbeat still holds the lease.
      await sleep(staleMs * 2);
      expect(await runHook()).toBe(false);

      // The agent finishes: the supervisor releases, and exactly one new survey is admitted.
      const first = agents()[0]!;
      process.kill(first.pid, "SIGTERM");
      await waitFor(() => !alive(first.ppid), "the supervisor outlived its agent");
      expect(await race()).toBe(1);
      await waitFor(
        () => agents().filter((a) => alive(a.pid)).length === 1,
        "the replacement survey never started"
      );

      // The supervisor dies without releasing: its lease goes stale, and one reclaimer wins.
      const second = agents().find((a) => alive(a.pid))!;
      process.kill(second.ppid, "SIGKILL");
      expect(await runHook()).toBe(false); // not stale yet
      await sleep(staleMs * 2);
      expect(await race()).toBe(1);
    } finally {
      for (const { pid, ppid } of agents()) {
        for (const p of [pid, ppid]) {
          try {
            process.kill(p, "SIGKILL");
          } catch {
            /* already exited */
          }
        }
      }
    }
  }, 60_000);

  afterAll(() => rmSync(lockDir, { recursive: true, force: true }));
});
