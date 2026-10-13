/**
 * Time formatting for the views.
 *
 * Three shapes, because timestamps in this console mean three different things:
 * an age ("4 hours ago"), an absolute moment ("Aug 19, 2025 at 14:38"), and a
 * position such as a read watermark, where only the shortest unambiguous form
 * will do.
 */

/** A timestamp rendered as an age, e.g. "4 hours ago". */
export function formatRelativeTime(dateStr: string): string {
  const then = new Date(dateStr).getTime();
  const diffSec = Math.round((Date.now() - then) / 1000);
  if (Math.abs(diffSec) < 60) return diffSec >= 0 ? "just now" : "in a moment";

  // value starts in seconds; each step divides by its factor to reach the next
  // unit up.
  const units: [number, Intl.RelativeTimeFormatUnit][] = [
    [60, "minute"],
    [60, "hour"],
    [24, "day"],
    [7, "week"],
    [4.345, "month"],
    [12, "year"],
  ];
  let value = diffSec;
  let unit: Intl.RelativeTimeFormatUnit = "second";
  for (const [factor, nextUnit] of units) {
    if (Math.abs(value) < factor) break;
    value = value / factor;
    unit = nextUnit;
  }
  return new Intl.RelativeTimeFormat("en", { numeric: "auto" }).format(-Math.round(value), unit);
}

/** A timestamp rendered as a full absolute date and time. */
export function formatAbsoluteDateTime(dateStr: string): string {
  const date = new Date(dateStr);
  return `${date.toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
    year: "numeric",
  })} at ${date.toLocaleTimeString("en-US", {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  })}`;
}

/**
 * A timestamp that marks a position rather than an age, such as a read
 * watermark.
 *
 * A relative rendering is wrong here: placed next to a genuine age it collapses
 * to the same string even when the two moments are hours apart, so the reader
 * sees what looks like the same fact printed twice. The output is therefore
 * always absolute, and only as long as it needs to be:
 *
 *   today        -> `14:38`
 *   this year    -> `19 Aug 14:38`
 *   earlier year -> `19 Aug 2025`
 *
 * Callers put the full date and time on the element's title, so shortening the
 * label loses nothing.
 */
export function formatWatermark(dateStr: string): string {
  const date = new Date(dateStr);
  const now = new Date();
  const time = date.toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
  if (date.toDateString() === now.toDateString()) return time;
  const dayMonth = date.toLocaleDateString(undefined, { day: "numeric", month: "short" });
  if (date.getFullYear() === now.getFullYear()) return `${dayMonth} ${time}`;
  return date.toLocaleDateString(undefined, {
    day: "numeric",
    month: "short",
    year: "numeric",
  });
}