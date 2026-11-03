/**
 * The shared runner's host-side contract, fully mocked (no docker, no network): the diagnostics
 * file must exist, host-owned and owner-only, BEFORE the container starts, and whatever the run
 * appends to it must come back verbatim in the collected diagnostics.
 */
import { ChildProcess, execFileSync, spawn, spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { PassThrough } from "node:stream";
import { expect, it, vi } from "vitest";
import { appendLogLine } from "../../src/core/log";

vi.mock("node:child_process", async (importOriginal) => ({
  ...(await importOriginal<typeof import("node:child_process")>()),
  execFileSync: vi.fn(),
  spawn: vi.fn(),
  spawnSync: vi.fn(),
}));

it("creates host-owned, owner-only diagnostics before Docker starts and collects appended logs", async () => {
  const root = mkdtempSync(join(tmpdir(), "wy-harness-test-"));
  const configPath = join(root, "config.json");
  const diagnostics = '{"event":"retain"}\n';
  writeFileSync(configPath, JSON.stringify({ apiUrl: "http://localhost:18888" }));
  vi.stubEnv("WENYUAN_AGENTS_E2E", "1");
  vi.stubEnv("WENYUAN_AGENTS_E2E_CONFIG", configPath);
  vi.stubEnv("WENYUAN_AGENTS_E2E_API_URL", "http://localhost:18888");
  vi.stubEnv("WENYUAN_AGENTS_E2E_API_TOKEN", "");

  vi.mocked(spawnSync).mockImplementation((command, args) => {
    if (command === "mkdir") {
      for (const directory of args!.slice(1)) if (directory) mkdirSync(directory, { recursive: true });
    }
    return { pid: 1, output: [], stdout: "", stderr: "", status: 0, signal: null };
  });
  vi.mocked(execFileSync).mockImplementation((command, args) => {
    const destination = command === "npm" ? args![2] : undefined;
    if (destination) writeFileSync(join(destination, "plugin.tgz"), "");
    return Buffer.alloc(0);
  });
  vi.mocked(spawn).mockImplementation((command, args) => {
    expect(command).toBe("docker");
    expect(args![0]).toBe("run");
    expect(args).toContain("WENYUAN_AGENTS_DIAG_FILE=/results/diagnostics.jsonl");
    const mount = args!.find((argument) => argument.endsWith(",dst=/results"));
    expect(mount).toBeDefined();
    const resultDir = mount!.slice("type=bind,src=".length, -",dst=/results".length);
    const diagnosticsPath = join(resultDir, "diagnostics.jsonl");
    const before = statSync(diagnosticsPath);
    expect(before.uid).toBe(statSync(resultDir).uid);
    expect(before.mode & 0o777).toBe(0o600);
    expect(readFileSync(diagnosticsPath, "utf8")).toBe("");

    appendLogLine(diagnosticsPath, diagnostics);
    const after = statSync(diagnosticsPath);
    expect(after.uid).toBe(before.uid);
    expect(after.ino).toBe(before.ino);
    expect(after.mode & 0o777).toBe(0o600);

    const child = new ChildProcess();
    child.stdout = new PassThrough();
    child.stderr = new PassThrough();
    queueMicrotask(() => child.emit("close", 0));
    return child;
  });

  try {
    const { runHarnessE2e } = await import("../../src/e2e/harness");
    const result = await runHarnessE2e({
      name: "test",
      harness: "test",
      installCommand: "wenyuan-agents install test",
      command: () => ["test"],
    });
    expect(spawn).toHaveBeenCalledOnce();
    expect(result.diagnostics).toBe(diagnostics);
  } finally {
    vi.unstubAllEnvs();
    vi.resetAllMocks();
    rmSync(root, { recursive: true, force: true });
  }
});
