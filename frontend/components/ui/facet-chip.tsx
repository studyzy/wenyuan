import * as React from "react";
import { Braces, Tag as TagIcon, Users, X } from "lucide-react";

/**
 * The one place tags, entities and metadata become chips.
 *
 * Colour is deliberately not how the kinds are told apart. Giving each kind its
 * own saturated fill reads as noise at this size — several filled chips in one
 * row compete with the content they annotate, and the duplication tells the
 * reader nothing the text did not already. Kinds are distinguished by form
 * instead:
 *
 *   tag       #name        pill, clickable where it filters
 *   entity    name         pill
 *   metadata  key=value    squared badge
 *
 * Every chip otherwise uses the same quiet neutral treatment. The brand accent
 * is spent on the single state worth calling out — an active filter — and reads
 * as an outline rather than another block of colour. The tokens live in the
 * stylesheet so both themes resolve through the dark block.
 */

export type FacetKind = "tag" | "entity" | "metadata";

export type ChipSize = "xs" | "sm" | "md";

const SIZE_CLASS: Record<ChipSize, string> = {
  xs: "text-[11px] px-2 py-[3px] gap-0.5",
  sm: "text-[13px] px-2.5 py-1 gap-1",
  md: "text-[13px] px-3 py-1.5 gap-1",
};

/** Pills for the name-like facets, a squared badge for metadata. */
const KIND_SHAPE: Record<FacetKind, string> = {
  tag: "rounded-full",
  entity: "rounded-full",
  metadata: "rounded-md",
};

/** Tonal variants of one base tone rather than separate colours. */
const KIND_TONE: Record<FacetKind, string> = {
  tag: "bg-chip-tag border-chip-tag-border",
  entity: "bg-chip-entity border-chip-entity-border",
  metadata: "bg-chip-meta border-chip-meta-border",
};

const CHIP_NEUTRAL = "text-chip-fg";

/** The exception, spent only on a chip worth calling out. */
const CHIP_ACCENT = "bg-transparent text-chip-accent border-chip-accent/30";

const CHIP_HOVER = "hover:border-chip-accent/40 hover:text-chip-accent";

const FACET_ICON: Record<FacetKind, typeof TagIcon> = {
  tag: TagIcon,
  entity: Users,
  metadata: Braces,
};

function chipClass(kind: FacetKind, size: ChipSize, active: boolean): string {
  // No max-width here: callers pass their own, and two competing max-width
  // utilities resolve by stylesheet order rather than by which was passed last,
  // so a base value would silently beat the caller's.
  return [
    "inline-flex items-center border font-medium leading-[1.2]",
    SIZE_CLASS[size],
    KIND_SHAPE[kind],
    active ? CHIP_ACCENT : `${KIND_TONE[kind]} ${CHIP_NEUTRAL}`,
  ].join(" ");
}

interface BaseChipProps {
  size?: ChipSize;
  className?: string;
  title?: string;
  /** Renders the chip as a button and fires this on click. */
  onClick?: () => void;
  /** Marks the chip as selected, for instance an active filter. */
  active?: boolean;
  /** Adds a trailing remove affordance. Not combinable with onClick. */
  onRemove?: () => void;
  removeLabel?: string;
  /** Truncate long values instead of wrapping. */
  truncate?: boolean;
}

function ChipShell({
  kind,
  size = "sm",
  active = false,
  className,
  title,
  onClick,
  onRemove,
  removeLabel,
  children,
}: BaseChipProps & { kind: FacetKind; children: React.ReactNode }) {
  const cls = `${chipClass(kind, size, active)} ${
    onClick ? `transition-colors cursor-pointer ${active ? "" : CHIP_HOVER}` : ""
  } ${className ?? ""}`;

  // Chips often sit inside a clickable row that opens a detail view. Clicking
  // the chip must not also trigger the row, so the stop lives here rather than
  // being re-implemented at every call site.
  const stop = (event: React.MouseEvent, fn: () => void) => {
    event.stopPropagation();
    fn();
  };

  if (onClick) {
    return (
      <button type="button" title={title} onClick={(event) => stop(event, onClick)} className={cls}>
        {children}
      </button>
    );
  }
  return (
    <span title={title} className={cls}>
      {children}
      {onRemove && (
        <button
          type="button"
          onClick={(event) => stop(event, onRemove)}
          aria-label={removeLabel}
          className="opacity-50 hover:opacity-100 transition-opacity ml-0.5 shrink-0"
        >
          <X className="h-3 w-3" />
        </button>
      )}
    </span>
  );
}

// Each chip renders its text as a single child of the shell: the shell's gap
// spaces the icon and the remove affordance, and would otherwise open a space
// inside the label itself.

/** A tag, always carrying the marker that reads as a tag anywhere. */
export function TagChip({ tag, truncate, ...rest }: BaseChipProps & { tag: string }) {
  return (
    <ChipShell kind="tag" {...rest}>
      <span className={truncate ? "truncate" : undefined}>
        <span className="opacity-70 select-none font-mono">#</span>
        {tag}
      </span>
    </ChipShell>
  );
}

/**
 * An identifier the system routes on, such as a bank alias.
 *
 * Shares the entity pill's form and tone rather than getting a kind of its own:
 * it is the same sort of thing visually — a bare name, not a key/value pair —
 * and the kinds are a closed set distinguished by form, so a fourth tone would
 * be a distinction without a difference. Monospace because, unlike an entity, it
 * is a literal string someone has to reproduce exactly.
 */
export function IdChip({ id, truncate, ...rest }: BaseChipProps & { id: string }) {
  return (
    <ChipShell kind="entity" {...rest}>
      <span className={`font-mono ${truncate ? "truncate" : ""}`}>{id}</span>
    </ChipShell>
  );
}

/** A resolved entity (person, place or thing) linked to a memory. */
export function EntityChip({ entity, truncate, ...rest }: BaseChipProps & { entity: string }) {
  return (
    <ChipShell kind="entity" {...rest}>
      <span className={truncate ? "truncate" : undefined}>{entity}</span>
    </ChipShell>
  );
}

/** One key/value pair of free-form document metadata. */
export function MetadataChip({
  entryKey,
  value,
  truncate,
  ...rest
}: BaseChipProps & { entryKey: string; value: string }) {
  return (
    <ChipShell kind="metadata" {...rest}>
      <span className={truncate ? "truncate" : "break-all"}>
        <span className="opacity-70">{entryKey}=</span>
        {value}
      </span>
    </ChipShell>
  );
}

/**
 * Legend for a column that mixes kinds: one labelled specimen per kind in the
 * exact treatment the rows use, so the rows can stay bare chips rather than
 * restating the category on every one.
 */
export function FacetLegend({ items }: { items: { kind: FacetKind; label: string }[] }) {
  return (
    <div className="flex items-center gap-1.5 normal-case tracking-normal">
      {items.map(({ kind, label }) => {
        const Icon = FACET_ICON[kind];
        return (
          <span key={kind} className={chipClass(kind, "sm", false)}>
            <Icon className="w-3 h-3 shrink-0" />
            {label}
          </span>
        );
      })}
    </div>
  );
}

/** Renders a list of tags, or nothing when the list is empty. */
export function TagList({
  tags,
  size = "sm",
  showLabel = false,
}: {
  tags: string[];
  size?: ChipSize;
  showLabel?: boolean;
}) {
  if (!tags || tags.length === 0) return null;

  return (
    <div>
      {showLabel && (
        <div className="mb-2 flex items-center gap-1 font-mono text-[10.5px] font-medium uppercase tracking-[0.11em] text-muted-foreground">
          <TagIcon className="w-3 h-3" />
          Tags
        </div>
      )}
      <div className="flex flex-wrap gap-1.5">
        {tags.map((tag, idx) => (
          <TagChip key={idx} tag={tag} size={size} />
        ))}
      </div>
    </div>
  );
}