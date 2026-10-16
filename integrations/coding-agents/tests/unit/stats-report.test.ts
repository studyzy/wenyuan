import { describe, expect, it } from "vitest";
import { stats } from "../../src/commands/stats";
import { usageLogPath } from "../../src/config/paths";
import { buildReport, formatReport, readUsageLog, recordFromLine } from "../../src/stats/usage-log";
import { captureOutput, withFakeHome, type InstallerRun } from "../helpers/fake-home";

/**
 * The usage report: what the read-only command prints for a log that is absent,
 * empty, half-written, or holding entries for one or several hosts.
 *
 * Nothing in this slice writes the log, so the reader has to tolerate all of
 * those shapes without failing.
 */

const LOG = ".wenyuan/usage.jsonl";

const ONE_HOST = [
  '{"host":"claude-code","tools":["recall"],"retrieved":true}',
  '{"host":"claude-code","tools":["recall","retain"]}',
  '{"host":"claude-code","tools":[]}',
].join("\n");

const SEVERAL_HOSTS = [
  '{"host":"claude-code","tool":"recall","retrieved":true}',
  '{"host":"codex","tools":["recall","retain"],"retrieved":true}',
  '{"host":"codex","turn":false}',
  '{"harness":"codebuddy","calls":["retain"]}',
].join("\n");

interface LogCase {
  name: string;
  lines: string[];
  records: number;
  hosts: string[];
}

const LOG_CASES: LogCase[] = [
  { name: "an absent log", lines: [], records: 0, hosts: [] },
  { name: "an empty log", lines: [""], records: 0, hosts: [] },
  { name: "entries for one host", lines: ONE_HOST.split("\n"), records: 3, hosts: ["claude-code"] },
  {
    name: "entries for several hosts",
    lines: SEVERAL_HOSTS.split("\n"),
    records: 4,
    hosts: ["claude-code", "codebuddy", "codex"],
  },
  {
    name: "a partially written trailing line",
    lines: [...ONE_HOST.split("\n"), '{"host":"claude-code","too'],
    records: 3,
    hosts: ["claude-code"],
  },
];

describe("reading the usage log", () => {
  it.each(LOG_CASES)("reads $name", async (testCase: LogCase) => {
    await withFakeHome({}, async (home) => {
      if (testCase.lines.length > 0) {
        await home.write(LOG, `${testCase.lines.join("\n")}\n`);
      }
      const records = await readUsageLog(home.path(LOG));
      expect(records).toHaveLength(testCase.records);
      expect([...new Set(records.map((record) => record.host))].sort()).toEqual(testCase.hosts);
    });
  });

  it("yields nothing for a log that does not exist", async () => {
    await withFakeHome({}, async (home) => {
      expect(await readUsageLog(home.path(LOG))).toEqual([]);
    });
  });

  it("counts a turn that says it is not one as no turn at all", () => {
    const record = recordFromLine('{"host":"codex","turn":false}');
    expect(record?.turns).toBe(0);
  });

  it("credits a turn that says it retrieved", () => {
    expect(recordFromLine('{"host":"codex","retrieved":true}')?.retrieved).toBe(true);
    expect(recordFromLine('{"host":"codex"}')?.retrieved).toBe(false);
  });

  it("skips a line that is not a complete record", () => {
    expect(recordFromLine('{"host":"codex","too')).toBeNull();
    expect(recordFromLine("not json at all")).toBeNull();
    expect(recordFromLine("[1,2]")).toBeNull();
  });
});

describe("the report", () => {
  it("summarizes turns, turns with a call, calls per turn and credited retrievals", () => {
    const report = buildReport([
      { host: "claude-code", turns: 1, calls: ["recall"], retrieved: true },
      { host: "claude-code", turns: 1, calls: ["recall", "retain"], retrieved: false },
      { host: "claude-code", turns: 1, calls: [], retrieved: false },
    ]);
    expect(report.hosts).toHaveLength(1);
    const usage = report.hosts[0];
    expect(usage?.host).toBe("claude-code");
    expect(usage?.turns).toBe(3);
    expect(usage?.turnsWithCall).toBe(2);
    expect(usage?.callsPerTurn).toBe(1);
    expect(usage?.creditedRetrievalTurns).toBe(1);
    expect(report.toolCounts).toEqual([
      { tool: "recall", count: 2 },
      { tool: "retain", count: 1 },
    ]);
  });

  it("prints one line per host followed by a per-tool breakdown", () => {
    const report = buildReport([
      { host: "claude-code", turns: 1, calls: ["recall"], retrieved: true },
      { host: "codex", turns: 1, calls: ["retain"], retrieved: false },
    ]);
    const text: string = formatReport(report, "/home/.wenyuan/usage.jsonl");
    expect(text).toContain("/home/.wenyuan/usage.jsonl");
    expect(text).toContain("claude-code: 1 turns, 1 with a call, 1 calls per turn, 1 credited retrieval turns");
    expect(text).toContain("codex: 1 turns, 1 with a call, 1 calls per turn, 0 credited retrieval turns");
    expect(text).toContain("recall: 1");
    expect(text).toContain("retain: 1");
  });
});

describe("the stats command", () => {
  it("names the usage log and exits zero when nothing has been recorded", async () => {
    await withFakeHome({}, async (home) => {
      const before: string[] = await home.list();
      const result: InstallerRun = await captureOutput(() => stats());
      expect(result.status).toBe(0);
      expect(result.stdout).toContain(usageLogPath());
      expect(await home.list()).toEqual(before);
    });
  });

  it("prints a per-host report and writes nothing", async () => {
    await withFakeHome({ files: { [LOG]: `${SEVERAL_HOSTS}\n` } }, async (home) => {
      const before: string[] = await home.list();
      const result: InstallerRun = await captureOutput(() => stats());
      expect(result.status).toBe(0);
      expect(result.stdout).toContain("claude-code:");
      expect(result.stdout).toContain("codex:");
      expect(result.stdout).toContain("codebuddy:");
      expect(result.stdout).toContain("recall");
      expect(await home.list()).toEqual(before);
    });
  });
});
