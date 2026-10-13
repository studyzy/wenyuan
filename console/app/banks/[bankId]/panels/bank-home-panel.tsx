"use client";

import { statsApi } from "../../../../lib/client/endpoints";
import { memoriesApi } from "../../../../lib/client/endpoints";
import { readNumber } from "../../../../lib/use-async-data";
import { useAsyncData } from "../../../../lib/use-async-data";
import { useTranslations } from "../../../../i18n";
import { Card, CardContent, CardHeader, CardTitle } from "../../../../components/ui/card";
import { PanelHeader, PanelState } from "./panel-state";

/**
 * Bank home.
 *
 * A glance at the bank's shape rather than a full view: the counts that say
 * whether anything has been retained, and nothing that would duplicate a
 * dedicated tab.
 */
export function BankHomePanel({ bankId }: { bankId: string }) {
  const t = useTranslations("panels.home");

  const stats = useAsyncData(() => statsApi.get(bankId), [bankId]);
  const memories = useAsyncData(() => memoriesApi.list(bankId), [bankId]);

  const cards: { label: string; value: string }[] = [
    { label: t("statMemories"), value: String(readNumber(stats.data, "total", 0)) },
    { label: t("statEntities"), value: String(readNumber(stats.data, "entity_count", 0)) },
    { label: t("statDocuments"), value: String(readNumber(stats.data, "document_count", 0)) },
  ];

  return (
    <div>
      <PanelHeader title={t("title")} description={t("description")} />
      <PanelState
        loading={stats.loading}
        error={stats.error}
        onRetry={stats.reload}
      >
        <div className="mb-6 grid grid-cols-1 gap-3 sm:grid-cols-3">
          {cards.map((card) => (
            <Card key={card.label}>
              <CardHeader className="p-4 pb-1">
                <CardTitle className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                  {card.label}
                </CardTitle>
              </CardHeader>
              <CardContent className="p-4 pt-0">
                <span className="text-[24px] font-semibold tabular-nums text-foreground">
                  {card.value}
                </span>
              </CardContent>
            </Card>
          ))}
        </div>
      </PanelState>

      <PanelState
        loading={memories.loading}
        error={memories.error}
        onRetry={memories.reload}
      >
        <p className="text-[13px] text-muted-foreground">
          {t("memoryHint", { count: readNumber(memories.data, "total", 0) })}
        </p>
      </PanelState>
    </div>
  );
}
