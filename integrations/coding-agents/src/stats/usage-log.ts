import { readFile } from "node:fs/promises";
import { usageLogPath } from "../config/paths";

/**
 * The local usage log the read-only command reports from.
 *
 * Nothing in this slice writes the log — its writer belongs to a later slice —
 * so the reader is deliberately defensive: a missing file, an empty file and a
 * partially written trailing line all yield nothing rather than failing, and a
 * record's shape is read by looking for the members a writer may plausibly
 * have used rather than by requiring one fixed shape.
 */

export interface UsageRecord {
  host: string;
  /** One record is one turn unless it says otherwise. */
  turns: number;
  /** The tools that turn called. */
  calls: string[];
  /** Whether the turn was credited with a retrieval. */
  retrieved: boolean;
}

export interface HostUsage {
  host: string;
  turns: number;
  turnsWithCall: number;
  callsPerTurn: number;
  creditedRetrievalTurns: number;
}

export interface ToolCount {
  tool: string;
  count: number;
}

export interface UsageReport {
  hosts: HostUsage[];
  toolCounts: ToolCount[];
}

const HOST_MEMBERS = ["host", "hostId", "harness"] as const;
const TOOL_MEMBERS = ["tool", "tools", "calls"] as const;
const CREDIT_MEMBERS = ["retrieved", "recall", "credited"] as const;
const UNKNOWN_HOST = "unknown";

/** Reads the log, yielding nothing for a missing, empty or unreadable file. */
export async function readUsageLog(path: string = usageLogPath()): Promise<UsageRecord[]> {
  let source: string;
  try {
    source = await readFile(path, "utf8");
  } catch {
    return [];
  }
  const records: UsageRecord[] = [];
  for (const line of source.split("\n")) {
    const record: UsageRecord | null = recordFromLine(line);
    if (record !== null) {
      records.push(record);
    }
  }
  return records;
}

/**
 * Parses one line. A line that is not a complete JSON object is skipped, which
 * is how a trailing line caught mid-write is tolerated.
 */
export function recordFromLine(line: string): UsageRecord | null {
  const trimmed: string = line.trim();
  if (!trimmed.startsWith("{")) {
    return null;
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch {
    return null;
  }
  if (!isRecord(parsed)) {
    return null;
  }
  return {
    host: firstString(parsed, HOST_MEMBERS) ?? UNKNOWN_HOST,
    turns: parsed["turn"] === false ? 0 : 1,
    calls: stringsFrom(parsed, TOOL_MEMBERS),
    retrieved: CREDIT_MEMBERS.some((member: string) => Boolean(parsed[member])),
  };
}

export function buildReport(records: readonly UsageRecord[]): UsageReport {
  const byHost: Map<string, HostUsage> = new Map();
  const toolTotals: Map<string, number> = new Map();
  for (const record of records) {
    const usage: HostUsage = byHost.get(record.host) ?? {
      host: record.host,
      turns: 0,
      turnsWithCall: 0,
      callsPerTurn: 0,
      creditedRetrievalTurns: 0,
    };
    usage.turns += record.turns;
    if (record.calls.length > 0) {
      usage.turnsWithCall += 1;
    }
    if (record.retrieved) {
      usage.creditedRetrievalTurns += 1;
    }
    byHost.set(record.host, usage);
    for (const tool of record.calls) {
      toolTotals.set(tool, (toolTotals.get(tool) ?? 0) + 1);
    }
  }
  const hosts: HostUsage[] = [...byHost.values()].sort((left, right) => left.host.localeCompare(right.host));
  for (const usage of hosts) {
    usage.callsPerTurn = usage.turns === 0 ? 0 : round(toolCountFor(records, usage.host) / usage.turns);
  }
  const toolCounts: ToolCount[] = [...toolTotals.entries()]
    .map(([tool, count]) => ({ tool, count }))
    .sort((left, right) => right.count - left.count || left.tool.localeCompare(right.tool));
  return { hosts, toolCounts };
}

export function formatReport(report: UsageReport, logPath: string = usageLogPath()): string {
  const lines: string[] = [`Recorded usage from ${logPath}`, ""];
  for (const usage of report.hosts) {
    lines.push(
      `  ${usage.host}: ${usage.turns} turns, ${usage.turnsWithCall} with a call, ` +
        `${usage.callsPerTurn} calls per turn, ${usage.creditedRetrievalTurns} credited retrieval turns`,
    );
  }
  lines.push("", "Calls per tool:");
  for (const tool of report.toolCounts) {
    lines.push(`  ${tool.tool}: ${tool.count}`);
  }
  return `${lines.join("\n")}\n`;
}

function toolCountFor(records: readonly UsageRecord[], host: string): number {
  return records
    .filter((record: UsageRecord) => record.host === host)
    .reduce((total: number, record: UsageRecord) => total + record.calls.length, 0);
}

function round(value: number): number {
  return Math.round(value * 100) / 100;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function firstString(source: Record<string, unknown>, members: readonly string[]): string | null {
  for (const member of members) {
    const value: unknown = source[member];
    if (typeof value === "string" && value.length > 0) {
      return value;
    }
  }
  return null;
}

function stringsFrom(source: Record<string, unknown>, members: readonly string[]): string[] {
  const found: string[] = [];
  for (const member of members) {
    const value: unknown = source[member];
    if (typeof value === "string" && value.length > 0) {
      found.push(value);
    } else if (Array.isArray(value)) {
      for (const item of value) {
        if (typeof item === "string" && item.length > 0) {
          found.push(item);
        }
      }
    }
  }
  return found;
}
