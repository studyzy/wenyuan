import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { memoryUsageCursorStore, recordUsage, usageFilePath } from "../../src/core/usage";
import { stats } from "../../src/commands/stats";

/**
 * The stats command over the shared usage ledger: a report when the runtime
 * has recorded turns, and the named-empty path when it has not. The ledger
 * path is redirected per test so the developer's real one is never read.
 */
describe("the stats command", () => {
  const dirs: string[] = [];

  function freshLedger(): string {
    const dir = mkdtempSync(join(tmpdir(), "wenyuan-stats-"));
    dirs.push(dir);
    process.env.WENYUAN_AGENTS_USAGE_FILE = join(dir, "usage.jsonl");
    return join(dir, "usage.jsonl");
  }

  afterEach(() => {
    delete process.env.WENYUAN_AGENTS_USAGE_FILE;
    for (const dir of dirs.splice(0)) rmSync(dir, { recursive: true, force: true });
    vi.restoreAllMocks();
  });

  it("names the ledger and reports success when nothing is recorded", async () => {
    const ledger = freshLedger();
    expect(usageFilePath()).toBe(ledger);
    const lines: string[] = [];
    vi.spyOn(process.stdout, "write").mockImplementation((line) => {
      lines.push(String(line));
      return true;
    });
    await expect(stats()).resolves.toBe(0);
    const report = lines.join("\n");
    expect(report).toContain("no recorded usage yet");
    expect(report).toContain(ledger);
  });

  it("prints a per-host report over what the runtime recorded", async () => {
    freshLedger();
    recordUsage({
      harness: "claude-code",
      sessionId: "session-stats",
      bankId: "coding-agent::stats-test",
      turns: [{ role: "user", content: "what changed here?" }],
      cursors: memoryUsageCursorStore(),
      lastTurnComplete: true,
    });
    const lines: string[] = [];
    vi.spyOn(process.stdout, "write").mockImplementation((line) => {
      lines.push(String(line));
      return true;
    });
    await expect(stats()).resolves.toBe(0);
    const report = lines.join("\n");
    expect(report).toContain("claude-code");
    expect(report).not.toContain("no recorded usage yet");
  });
});
