import { HOST_LIFECYCLES, orderedRegistrations } from "../../src/hosts/lifecycle";
import { commandEntriesOf, HOOKS_CONTAINER_KEY, MATCHER_GROUP_KEY } from "../../src/hosts/hook-shape";
import { OWNERSHIP_MARKER } from "../../src/write/marker";
import type { JsonDocument } from "./fake-home";

/**
 * Test-only readers for the per-host lifecycle registration shape.
 *
 * The lifecycle is declared once in the package; the suites here read the
 * written host config through that same declaration rather than hard-coding
 * event spellings or the nesting depth, so a suite cannot drift from what the
 * installer writes.
 */

/** A parsed host config, keyed by top-level member name. */
export type HostConfig = JsonDocument;

/** The event spellings one host declares, in the shared stage order. */
export function declaredEvents(hostId: string): string[] {
  const lifecycle = HOST_LIFECYCLES[hostId];
  if (!lifecycle) {
    throw new Error(`no lifecycle is declared for host "${hostId}"`);
  }
  return orderedRegistrations(lifecycle).map(({ registration }) => registration.event);
}

/** The value written on one event key, or undefined when the key is absent. */
export function eventValue(config: HostConfig, event: string): unknown {
  const hooks: unknown = config[HOOKS_CONTAINER_KEY];
  if (hooks === null || typeof hooks !== "object" || Array.isArray(hooks)) {
    return undefined;
  }
  return (hooks as JsonDocument)[event];
}

/** The command entries written on one event key, decoded through the shape. */
export function eventCommandEntries(config: HostConfig, event: string): { command: string; timeout: number }[] {
  return commandEntriesOf(eventValue(config, event));
}

/** How many of one event's command entries carry the ownership marker. */
export function ownEntryCount(config: HostConfig, event: string): number {
  return eventCommandEntries(config, event).filter((entry) => entry.command.includes(OWNERSHIP_MARKER)).length;
}

/** The command paths written on one event key. */
export function eventCommands(config: HostConfig, event: string): string[] {
  return eventCommandEntries(config, event).map((entry) => entry.command);
}

/** Re-exported so a suite reading the nested shape names it from one place. */
export { HOOKS_CONTAINER_KEY, MATCHER_GROUP_KEY };