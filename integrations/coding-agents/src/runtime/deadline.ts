import { hookPathFor } from "../hosts/hooks";
import { hostLifecycle, orderedRegistrations } from "../hosts/lifecycle";
import { budgetMilliseconds } from "../hosts/lifecycle";
import { runtimeDirectory } from "./layout";

/**
 * Deadline arithmetic for the packaged hooks.
 *
 * A host enforces a budget on every hook it launches; a hook that runs to that
 * budget is killed. The memory-facing stages therefore impose their own
 * deadline inside the host's, so the hook yields in an orderly way first.
 *
 * The stop stage keeps a fixed positive margin inside its budget because it
 * writes a whole conversation back; the interactive stages take their whole
 * budget, since they are expected to return well before it.
 */

/** The fixed positive margin the stop hook keeps inside its budget, in seconds. */
export const STOP_DEADLINE_MARGIN_SECONDS = 5;

/** The smallest deadline the stop stage will self-impose, in milliseconds. */
const MINIMUM_STOP_DEADLINE_MS = 1;

/**
 * The stage's self-imposed deadline in milliseconds, always strictly smaller
 * than the host's budget for that stage. The margin is subtracted in the
 * declared unit and the result is floored so a misconfigured host cannot yield
 * a zero or negative deadline.
 */
export function stageDeadlineMs(hostId: string, stage: string): number {
  const registration = hostLifecycle(hostId)[stage];
  if (registration === undefined) {
    throw new Error(`host "${hostId}" declares no "${stage}" stage`);
  }
  const budgetMs: number = budgetMilliseconds(registration);
  if (stage !== "stop") {
    return budgetMs;
  }
  const marginMs: number =
    registration.budgetUnit === "seconds"
      ? STOP_DEADLINE_MARGIN_SECONDS * 1000
      : STOP_DEADLINE_MARGIN_SECONDS;
  return Math.max(MINIMUM_STOP_DEADLINE_MS, budgetMs - marginMs);
}

/** The absolute path of the packaged hook script a host stage declares. */
export function stageScriptPath(hostId: string, stage: string): string {
  const registration = hostLifecycle(hostId)[stage];
  if (registration === undefined) {
    throw new Error(`host "${hostId}" declares no "${stage}" stage`);
  }
  return hookPathFor(registration.scriptFile, runtimeDirectory());
}

/** The registered host ids, in the shared declaration order. */
export function declaredStages(hostId: string): string[] {
  return orderedRegistrations(hostLifecycle(hostId)).map(({ stage }) => stage);
}