import { withBasePath } from "../../lib/base-path";
import { cn } from "../../lib/utils";
import { resolveHarnessLogo } from "../../lib/harness-logo";

/**
 * The mark of the coding agent that wrote a document.
 *
 * This is a mark rather than a chip: it sits inline beside the value it
 * qualifies and carries no border or fill of its own, so a table row spends one
 * glyph on it instead of another badge competing with the facet chips.
 *
 * A registered agent with no shipped asset renders its label instead, and an
 * unrecognised one renders nothing at all — callers still show the raw value as
 * ordinary metadata, so a new agent is never silently invisible.
 */
export function HarnessLogo({
  harness,
  size = 16,
  className,
  titlePrefix,
}: {
  harness: string | null | undefined;
  /** Box size in px; the mark is contained so non-square art keeps its ratio. */
  size?: number;
  className?: string;
  /** Prefix for the tooltip, e.g. "Harness" renders as "Harness: Claude Code". */
  titlePrefix?: string;
}) {
  const logo = resolveHarnessLogo(harness);
  if (!logo) return null;

  const title = titlePrefix ? `${titlePrefix}: ${logo.label}` : logo.label;

  if (!logo.src) {
    return (
      <span
        title={title}
        className={cn("shrink-0 text-muted-foreground", className)}
        style={{ fontSize: Math.max(10, size - 4) }}
      >
        {logo.label}
      </span>
    );
  }

  return (
    // A plain img: a static local asset of a few KB gains nothing from an image
    // loader and a layout wrapper at this size.
    <img
      src={withBasePath(logo.src)}
      alt={logo.label}
      title={title}
      width={size}
      height={size}
      className={cn(
        "shrink-0 rounded-[3px] object-contain",
        logo.invertOnDark && "dark:invert",
        className,
      )}
      style={{ width: size, height: size }}
    />
  );
}
