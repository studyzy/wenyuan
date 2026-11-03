import { say } from "../cli/log";
import { formatUsageReport, readUsage, usageFilePath } from "../core/usage";

/**
 * The stats command: strictly read-only.
 *
 * It reads the shared usage ledger the core runtime writes (every host lane
 * records through core/usage), prints a per-host report and touches nothing
 * else — no staging, no host config, nothing sent anywhere. It is dispatched
 * before any staging or host work so it cannot write even by accident, and it
 * reports success when there is nothing recorded yet, naming the ledger an
 * operator can look at.
 */
export async function stats(): Promise<number> {
  const ledger = usageFilePath();
  const usage = readUsage(ledger);
  if (usage.length === 0) {
    say(`no recorded usage yet: nothing has been written to ${ledger}`);
    return 0;
  }
  say(formatUsageReport(usage, ledger));
  return 0;
}
