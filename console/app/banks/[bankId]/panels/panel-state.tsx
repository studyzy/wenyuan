"use client";

import type { ReactNode } from "react";

import { useTranslations } from "../../../../i18n";
import { Button } from "../../../../components/ui/button";
import { Spinner } from "../../../../components/ui/spinner";

/**
 * The three states every data panel has to render besides its content.
 *
 * Centralised because the panels otherwise drift: one would show a bare spinner
 * with no error path, another an empty message with no retry. Passing the async
 * state straight through also means a panel cannot accidentally render content
 * while it is still loading.
 */
export function PanelState({
  loading,
  error,
  empty,
  onRetry,
  children,
}: {
  loading: boolean;
  error: boolean;
  /** True when the load succeeded but produced nothing to show. */
  empty?: boolean;
  onRetry?: () => void;
  children?: ReactNode;
}) {
  const t = useTranslations("common.panel");

  if (loading) {
    return (
      <div className="flex justify-center py-16">
        <Spinner size="lg" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="flex flex-col items-center gap-3 py-16">
        <p className="text-[13px] text-muted-foreground">{t("loadFailed")}</p>
        {onRetry && (
          <Button variant="outline" size="sm" onClick={onRetry}>
            {t("retry")}
          </Button>
        )}
      </div>
    );
  }

  if (empty) {
    return (
      <div className="rounded-[16px] border border-border bg-card px-6 py-16 text-center">
        <p className="text-[13px] text-muted-foreground">{t("empty")}</p>
      </div>
    );
  }

  return <>{children}</>;
}

/** A panel heading with an optional action on the right. */
export function PanelHeader({
  title,
  description,
  action,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
}) {
  return (
    <div className="mb-4 flex items-start justify-between gap-4">
      <div className="flex flex-col gap-0.5">
        <h2 className="text-[16px] font-semibold leading-[22px] tracking-tight text-foreground">
          {title}
        </h2>
        {description && <p className="text-[13px] text-muted-foreground">{description}</p>}
      </div>
      {action}
    </div>
  );
}

/** A labelled key/value row, for the detail-style readouts. */
export function DetailRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5 border-b border-border/60 py-2 last:border-b-0">
      <dt className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
        {label}
      </dt>
      <dd className="text-[13px] text-foreground">{children}</dd>
    </div>
  );
}