import { say } from "../cli/log";
import { usageLogPath } from "../config/paths";
import { buildReport, formatReport, readUsageLog } from "../stats/usage-log";

/**
 * The stats command: strictly read-only.
 *
 * It reads the local usage log, prints a per-host report and touches nothing
 * else — no staging, no host config, nothing sent anywhere. It is dispatched
 * before any staging or host work so it cannot write even by accident, and it
 * reports success when there is nothing recorded yet, naming the log an
 * operator can look at.
 */
export async function stats(): Promise<number> {
  const logPath: string = usageLogPath();
  const records = await readUsageLog(logPath);
  if (records.length === 0) {
    say(`no recorded usage yet: nothing has been written to ${logPath}`);
    return 0;
  }
  const report = buildReport(records);
  say(formatReport(report, logPath));
  return 0;
}
