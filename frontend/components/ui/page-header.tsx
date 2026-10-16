import * as React from "react";

import { cn } from "../../lib/utils";

/**
 * The one page heading treatment.
 *
 * Every view used to restate its own `<h1 className="text-3xl font-bold">` plus
 * a muted paragraph, so the type scale, the spacing and the eyebrow drifted
 * between views. This pins them in one place: an optional monospace eyebrow
 * naming the surface, the display-face title, and a supporting line.
 *
 * The title deliberately carries no colour of its own — the gradient treatment
 * is reserved for `GradientText`, so the ordinary heading stays readable and the
 * accent means something where it is spent.
 */
function PageHeader({
  eyebrow,
  title,
  description,
  actions,
  children,
  className,
}: {
  eyebrow?: React.ReactNode;
  title: React.ReactNode;
  description?: React.ReactNode;
  /** Controls floated to the right of the heading block. */
  actions?: React.ReactNode;
  /** Extra rows rendered under the heading block, full width. */
  children?: React.ReactNode;
  className?: string;
}) {
  return (
    <header className={cn("mb-6", className)}>
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          {eyebrow && <Eyebrow>{eyebrow}</Eyebrow>}
          <h1 className="font-heading text-[26px] leading-tight font-semibold tracking-[-0.02em] text-foreground">
            {title}
          </h1>
          {description && (
            <p className="mt-1.5 max-w-[70ch] text-sm text-muted-foreground">{description}</p>
          )}
        </div>
        {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
      </div>
      {children}
    </header>
  );
}

/**
 * A small monospace label above a heading. Monospace is the point: eyebrows name
 * an identifier — a view slug, a bank id, a subsystem — and the mono face reads
 * as a label rather than as the first sentence of the page.
 */
function Eyebrow({ children, className }: { children: React.ReactNode; className?: string }) {
  return (
    <div
      className={cn(
        "mb-1.5 inline-flex items-center gap-1.5 font-mono text-[11px] font-medium uppercase tracking-[0.12em] text-muted-foreground",
        className,
      )}
    >
      <span aria-hidden className="h-px w-4 bg-primary/60" />
      {children}
    </div>
  );
}

/** Emphasis for a single word or figure inside a heading or metric. */
function GradientText({ children, className }: { children: React.ReactNode; className?: string }) {
  return <span className={cn("text-brand-gradient", className)}>{children}</span>;
}

export { PageHeader, Eyebrow, GradientText };