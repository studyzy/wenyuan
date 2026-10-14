"use client";

import { useState } from "react";

import { useTranslations } from "../../../../i18n";
import { cn } from "../../../../lib/utils";
import { TagList } from "../../../../components/ui/facet-chip";
import { JsonViewer } from "../../../../components/ui/json-viewer";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../../../../components/ui/tabs";

type RecallResultItem = {
  id?: string;
  text?: string;
  type?: string;
  tags?: string[];
  entities?: string[];
  scores?: {
    final?: number;
    reranker?: number;
    semantic?: number;
    keyword?: number;
  };
};

type RecallPayload = {
  results?: RecallResultItem[];
  truncated?: boolean;
  trace?: Record<string, unknown>;
};

/**
 * A recall response, as a ranked list or as the raw envelope.
 *
 * The list is the readable projection: result text, its type, the tags and
 * entities it carries, and the score breakdown. The raw tab keeps the envelope
 * reachable, because the response is the contract and a client reading it needs
 * the fields a projection necessarily drops.
 *
 * Both views read the same fetched payload, so switching between them costs no
 * second call.
 */
export function RecallResults({ payload }: { payload: unknown }) {
  const t = useTranslations("panels.recall");
  const data = (payload ?? {}) as RecallPayload;
  const results = Array.isArray(data.results) ? data.results : [];

  return (
    <Tabs defaultValue="list">
      <TabsList>
        <TabsTrigger value="list">
          {t("tabList", { total: results.length })}
        </TabsTrigger>
        <TabsTrigger value="raw">{t("tabRaw")}</TabsTrigger>
      </TabsList>

      <TabsContent value="list">
        {results.length === 0 ? (
          <div className="rounded-[16px] border border-border bg-card px-6 py-12 text-center">
            <p className="text-[13px] text-muted-foreground">{t("noResults")}</p>
          </div>
        ) : (
          <>
            {data.truncated && (
              <p className="mb-2 text-[12px] text-muted-foreground">{t("truncated")}</p>
            )}
            <ol className="flex flex-col gap-2">
              {results.map((item, index) => (
                <ResultCard key={item.id ?? index} item={item} rank={index + 1} />
              ))}
            </ol>
          </>
        )}
      </TabsContent>

      <TabsContent value="raw">
        <JsonViewer value={payload} className="bg-muted" />
      </TabsContent>
    </Tabs>
  );
}

/** One ranked result with its score breakdown. */
function ResultCard({ item, rank }: { item: RecallResultItem; rank: number }) {
  const t = useTranslations("panels.recall");
  const tags = Array.isArray(item.tags) ? item.tags : [];
  const entities = Array.isArray(item.entities) ? item.entities : [];

  return (
    <li className="rounded-[14px] border border-border bg-card px-4 py-3">
      <div className="mb-1.5 flex items-start justify-between gap-3">
        <div className="flex min-w-0 items-baseline gap-2">
          <span className="shrink-0 font-mono text-[11px] text-muted-foreground">#{rank}</span>
          {item.type && (
            <span className="shrink-0 rounded-full bg-chip-tag px-2 py-0.5 text-[11px]">
              {item.type}
            </span>
          )}
        </div>
        <ScoreBreakdown scores={item.scores} label={t("scoreFinal")} />
      </div>

      <p className="whitespace-pre-wrap break-words text-[13px] leading-relaxed">
        {item.text ?? ""}
      </p>

      {(tags.length > 0 || entities.length > 0) && (
        <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1">
          {tags.length > 0 && <TagList tags={tags.slice(0, 5)} size="xs" />}
          {entities.length > 0 && (
            <span className="truncate text-[11px] text-muted-foreground">
              {entities.slice(0, 5).join(" · ")}
            </span>
          )}
        </div>
      )}

      {item.id && (
        <span className="mt-1.5 block truncate font-mono text-[10px] text-muted-foreground">
          {item.id}
        </span>
      )}
    </li>
  );
}

/**
 * The stages that scored one result.
 *
 * A stage that did not surface the result reports no value, so an absent stage
 * is omitted rather than shown as zero — zero would read as "scored badly"
 * where the truth is "never considered".
 */
function ScoreBreakdown({
  scores,
  label,
}: {
  scores?: RecallResultItem["scores"];
  label: string;
}) {
  if (!scores) return null;
  const stages: Array<[string, number | undefined]> = [
    [label, scores.final],
    ["reranker", scores.reranker],
    ["semantic", scores.semantic],
    ["keyword", scores.keyword],
  ];
  const present = stages.filter(([, value]) => typeof value === "number");
  if (present.length === 0) return null;
  return (
    <div className="flex shrink-0 flex-wrap justify-end gap-x-2 gap-y-0.5">
      {present.map(([name, value], index) => (
        <span
          key={name}
          className={cn(
            "font-mono text-[10px]",
            index === 0 ? "text-foreground" : "text-muted-foreground",
          )}
        >
          {name} {(value as number).toFixed(3)}
        </span>
      ))}
    </div>
  );
}