import { HOST_LIFECYCLES, registrationFor } from "../hosts/lifecycle";
import type { LifecycleRegistration } from "../hosts/lifecycle-types";
import { payloadString } from "./hook-contract";

/**
 * How a packaged hook resolves which host it is firing for.
 *
 * A host launches the hook by absolute path and passes no flag saying which
 * host it is, so the hook infers it: from the deployment harness variable the
 * host sets, or from a host-identifying member the lifecycle payload carries.
 * The three entry points share this one resolver so they cannot disagree.
 */

/** The host-identifying environment variable the deployment harness sets. */
export const HARNESS_ENV = "HINDSIGHT_MCP_HARNESS";

/**
 * The host this firing belongs to. A payload member naming a declared host
 * wins; the harness variable is the fallback; a firing that names neither is
 * the first declared host, so a hook always has a lifecycle to resolve.
 */
export function resolveHostId(
  payload: Record<string, unknown>,
  environment: NodeJS.ProcessEnv = process.env,
): string | undefined {
  const declared: string[] = Object.keys(HOST_LIFECYCLES);
  const fromPayload: string | undefined = payloadString(payload, "host", "host_id", "harness");
  if (fromPayload !== undefined && HOST_LIFECYCLES[fromPayload] !== undefined) {
    return fromPayload;
  }
  const fromEnv: string | undefined = environment[HARNESS_ENV];
  if (fromEnv !== undefined && HOST_LIFECYCLES[fromEnv] !== undefined) {
    return fromEnv;
  }
  return declared[0];
}

/** Re-exported so an entry point resolves its stage from one shared module. */
export { registrationFor };
export type { LifecycleRegistration };