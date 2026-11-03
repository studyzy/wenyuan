/**
 * Hashed cron fields — the "H" extension.
 *
 * One pageTriggerCron is shared by every page in every bank running this package, copied verbatim.
 * A literal "0 3 * * *" therefore schedules ALL of them on the ONE minute it names, on the worker
 * pool that also serves retain. Writing "H" in a field means "pick this field's value by hashing
 * the page", so every page gets its own stable slot instead of the one the config literally says.
 *
 * Jenkins' syntax, borrowed rather than invented. It composes with the rest of the expression:
 *   "H H * * *"      once a day, at this page's own minute and hour
 *   "H * * * *"      once an hour, at this page's own minute
 *   "H 3 * * *"      daily at 03:MM — spread within the hour the operator chose
 *   "H H(0-5) * * *" daily, spread across the night only
 *   "0 3 * * *"      unchanged: no "H", no hashing
 *
 * "H" never leaves this package: expandCronHash resolves it to an ordinary expression before the
 * trigger is sent, because the server parses refresh_cron as standard cron.
 */
import { createHash } from "node:crypto";
import type { PageTrigger } from "./pages";

const CRON_FIELD_RANGES: readonly (readonly [number, number])[] = [
  [0, 59], // minute
  [0, 23], // hour
  [1, 31], // day of month
  [1, 12], // month
  [0, 6], // day of week
];

const HASHED_FIELD = /^H(?:\((\d+)-(\d+)\))?$/;

/**
 * Does this expression ask for hashing at all? Any field STARTING with H counts, well-formed or
 * not: no standard cron field begins with H, so "Hx" is a typo in this package's syntax, and
 * claiming it here reports a malformed hashed field rather than an opaque server parse error.
 */
export function isHashedCron(cron: string): boolean {
  return /(^|\s)H/.test(cron);
}

/**
 * The five fields of "cron" when every H in it is well-formed, else undefined. Only the H fields
 * are checked — ordinary cron syntax is the server's to validate, as it already does.
 */
export function parseHashedCron(cron: string): string[] | undefined {
  const fields = cron.trim().split(/\s+/);
  if (fields.length !== CRON_FIELD_RANGES.length) return undefined;
  for (const [i, field] of fields.entries()) {
    if (!field.startsWith("H")) continue;
    const m = HASHED_FIELD.exec(field);
    if (!m) return undefined;
    if (m[1] === undefined) continue;
    const lo = Number(m[1]);
    const hi = Number(m[2]);
    const range = CRON_FIELD_RANGES[i];
    if (!range) return undefined;
    if (lo > hi || lo < range[0] || hi > range[1]) return undefined;
  }
  return fields;
}

/** "seed"'s own value in [lo, hi] — stable across machines, processes and releases. The field
 *  index is hashed alongside the seed so "H H * * *" does not derive minute and hour from one
 *  number: the two would move together across pages, collapsing 1440 daily slots towards 60. */
function hashedValue(seed: string, field: number, lo: number, hi: number): number {
  const digest = createHash("sha256").update(seed + "\u0000" + String(field)).digest();
  return lo + (digest.readUInt32BE(0) % (hi - lo + 1));
}

/**
 * "cron" with each H replaced by "seed"'s own value for that field — an ordinary expression.
 * Returns the input untouched when it holds no H, and when an H in it is malformed: a bad
 * expression is the server's to reject, not ours to rewrite into a schedule nobody asked for.
 */
export function expandCronHash(cron: string, seed: string): string {
  if (!isHashedCron(cron)) return cron;
  const fields = parseHashedCron(cron);
  if (!fields) return cron;
  return fields
    .map((field, i) => {
      const m = HASHED_FIELD.exec(field);
      if (!m) return field;
      const range = CRON_FIELD_RANGES[i];
      if (!range) return field;
      const [lo, hi] = m[1] === undefined ? range : [Number(m[1]), Number(m[2])];
      return String(hashedValue(seed, i, lo, hi));
    })
    .join(" ");
}

/**
 * The trigger to send for ONE page, resolving any H against that page's identity. Applied where a
 * page is created — the only place the identity exists. The seed is bank + page name, the pair
 * that identifies a page across runs, so a page keeps its slot while it keeps its name and two
 * banks seeded from the same config land on different ones. Hashing distributes; it does not
 * partition, so two pages CAN collide.
 */
export function pageTriggerFor(trigger: PageTrigger, bank: string, page: string): PageTrigger {
  const cron = trigger.refresh_cron;
  if (!cron || !isHashedCron(cron)) return trigger;
  return { ...trigger, refresh_cron: expandCronHash(cron, bank + "\u0000" + page) };
}
