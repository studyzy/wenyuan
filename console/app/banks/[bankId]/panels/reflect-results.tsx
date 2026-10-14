"use client";

import { useState } from "react";

import { useTranslations } from "../../../../i18n";
import { TagList } from "../../../../components/ui/facet-chip";
import { JsonViewer } from "../../../../components/ui/json-viewer";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../../../../components/ui/tabs";

type CitedFact = { id?: string; text?: string; type?: string; tags?: string[] };
type CitedMentalModel = { id?: string; name?: string; text?: string };
type CitedDirective = { id?: string; text?: string };

type ReflectPayload = {
  text?: string;
  based_on?: {
    memories?: CitedFact[];
    mental_models?: CitedMentalModel[];
    directives?: CitedDirective[];
  };
  structured_output?: Record<string, unknown>;
  structured_output_error?: string;
  usage?: {
    input_tokens?: number;
    output_tokens?: number;
    total_tokens?: number;
  };
};

/**
 * A reflection response: the answer, the evidence it rests on, and the raw
 * envelope.
 *
 * The text is the answer itself and is shown first and unsummarised — it is
 * what the caller asked for. The evidence is grouped by the kind of record it
 * came from, because a memory, a mental model and a directive are read
 * differently and mixing them would obscure which one grounded a claim.
 */
export function ReflectResults({ payload }: { payload: unknown }) {
  const t = useTranslations("panels.reflect");
  const data = (payload ?? {}) as ReflectPayload;

  const memories = data.based_on?.memories ?? [];
  const models = data.based_on?.mental_models ?? [];
  const directives = data.based_on?.directives ?? [];
  const evidenceCount = memories.length + models.length + directives.length;

  return (
    <Tabs defaultValue="answer">
      <TabsList>
        <TabsTrigger value="answer">{t("tabAnswer")}</TabsTrigger>
        <TabsTrigger value="evidence">
          {t("tabEvidence", { total: evidenceCount })}
        </TabsTrigger>
        <TabsTrigger value="raw">{t("tabRaw")}</TabsTrigger>
      </TabsList>

      <TabsContent value="answer">
        <article className="rounded-[16px] border border-border bg-card px-4 py-3">
          {data.text ? (
            <p className="whitespace-pre-wrap break-words text-[14px] leading-relaxed">
              {data.text}
            </p>
          ) : (
            <p className="text-[13px] text-muted-foreground">{t("noAnswer")}</p>
          )}
        </article>

        {data.usage && (
          <p className="mt-2 text-[11px] text-muted-foreground">
            {t("usage", {
              input: data.usage.input_tokens ?? 0,
              output: data.usage.output_tokens ?? 0,
              total: data.usage.total_tokens ?? 0,
            })}
          </p>
        )}

        {data.structured_output_error && (
          <p role="alert" className="mt-2 text-[12px] text-destructive">
            {data.structured_output_error}
          </p>
        )}

        {data.structured_output && (
          <div className="mt-4">
            <h3 className="mb-2 text-[13px] font-semibold">{t("structuredOutput")}</h3>
            <JsonViewer value={data.structured_output} className="bg-muted" />
          </div>
        )}
      </TabsContent>

      <TabsContent value="evidence">
        {evidenceCount === 0 ? (
          <div className="rounded-[16px] border border-border bg-card px-6 py-12 text-center">
            <p className="text-[13px] text-muted-foreground">{t("noEvidence")}</p>
          </div>
        ) : (
          <div className="flex flex-col gap-5">
            <EvidenceGroup
              title={t("evidenceMemories", { total: memories.length })}
              items={memories.map((item) => ({
                key: item.id ?? item.text ?? "",
                label: item.type,
                text: item.text ?? "",
                tags: item.tags,
              }))}
            />
            <EvidenceGroup
              title={t("evidenceModels", { total: models.length })}
              items={models.map((item) => ({
                key: item.id ?? item.name ?? "",
                label: item.name,
                text: item.text ?? "",
              }))}
            />
            <EvidenceGroup
              title={t("evidenceDirectives", { total: directives.length })}
              items={directives.map((item) => ({
                key: item.id ?? item.text ?? "",
                text: item.text ?? "",
              }))}
            />
          </div>
        )}
      </TabsContent>

      <TabsContent value="raw">
        <JsonViewer value={payload} className="bg-muted" />
      </TabsContent>
    </Tabs>
  );
}

/** One kind of cited evidence. An empty group is omitted, not shown as a heading. */
function EvidenceGroup({
  title,
  items,
}: {
  title: string;
  items: Array<{ key: string; label?: string; text: string; tags?: string[] }>;
}) {
  if (items.length === 0) return null;
  return (
    <section>
      <h3 className="mb-2 text-[13px] font-semibold">{title}</h3>
      <ul className="flex flex-col gap-2">
        {items.map((item, index) => (
          <li
            key={item.key || index}
            className="rounded-[12px] border border-border bg-card px-3 py-2.5"
          >
            {item.label && (
              <span className="mb-1 inline-block rounded-full bg-chip-tag px-2 py-0.5 text-[11px]">
                {item.label}
              </span>
            )}
            <p className="whitespace-pre-wrap break-words text-[13px] leading-relaxed">
              {item.text}
            </p>
            {item.tags && item.tags.length > 0 && (
              <div className="mt-1.5">
                <TagList tags={item.tags.slice(0, 5)} size="xs" />
              </div>
            )}
          </li>
        ))}
      </ul>
    </section>
  );
}