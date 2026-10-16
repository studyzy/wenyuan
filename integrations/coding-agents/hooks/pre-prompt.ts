import { LIFECYCLE_EVENTS, type LifecycleEventId } from "../src/hosts/hooks";

/**
 * The packaged pre-prompt hook, launched before a user prompt is submitted.
 *
 * The event it serves comes from the one shared declaration, so install,
 * uninstall and this entry point cannot disagree about which events exist.
 * The hook's runtime behavior belongs to a later slice: for now it exists so
 * the host has something to launch, and it succeeds without touching anything.
 */
const EVENT_ID: LifecycleEventId = "pre-prompt";

function main(): number {
  const known: boolean = LIFECYCLE_EVENTS.some((candidate) => candidate.id === EVENT_ID);
  if (!known) {
    process.stderr.write(`unknown lifecycle event: ${EVENT_ID}\n`);
    return 1;
  }
  return 0;
}

process.exit(main());
