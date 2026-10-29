"use client";

import { useEffect, useState } from "react";
import { useTranslations } from "next-intl";
import { adminApi } from "@/lib/admin-api";
import { ConsoleNav } from "@/components/console-nav";
import { PageHeader } from "@/components/ui/page-header";
import { Card, CardContent } from "@/components/ui/card";

const LABELS = ["total", "active", "suspended", "provisioning", "deleted"] as const;

export function OverviewView() {
  const t = useTranslations("tenants.overview");
  const [counts, setCounts] = useState<Record<string, number>>({});
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    adminApi
      .overview()
      .then(setCounts)
      .catch((err: unknown) =>
        setError(err instanceof Error ? err.message : t("failed")),
      );
  }, [t]);

  return (
    <div className="min-h-screen bg-background">
      <ConsoleNav />
      <main className="mx-auto w-full max-w-5xl px-6 py-8">
        <PageHeader eyebrow="management" title={t("title")} description={t("description")} />
        {error && <p className="mb-4 text-sm text-destructive">{error}</p>}
        <div className="grid grid-cols-2 gap-3 md:grid-cols-5">
          {LABELS.map((label) => (
            <Card key={label}>
              <CardContent className="py-5">
                <div className="text-[11px] uppercase tracking-[0.12em] text-muted-foreground">
                  {t(label)}
                </div>
                <div className="mt-1 font-heading text-[28px] font-semibold">
                  {counts[label] ?? 0}
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      </main>
    </div>
  );
}
