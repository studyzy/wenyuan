"use client";

import { useState } from "react";

import { knowledgeBaseApi } from "../../../../lib/client/endpoints";
import { readItems, useAsyncData } from "../../../../lib/use-async-data";
import { useTranslations } from "../../../../i18n";
import { Input } from "../../../../components/ui/input";
import { PanelHeader, PanelState } from "./panel-state";
import { MentalModelsView } from "./mental-models-view";
import type { KnowledgeSubTab } from "../../../../lib/bank-detail";

type KnowledgeNode = { id?: string; title?: string; name?: string; children?: KnowledgeNode[] };

/** Flatten the tree so a search can reach any depth. */
function flatten(nodes: KnowledgeNode[], out: KnowledgeNode[] = []): KnowledgeNode[] {
  for (const node of nodes) {
    out.push(node);
    if (node.children?.length) flatten(node.children, out);
  }
  return out;
}

/**
 * Knowledge base.
 *
 * The tree arrives nested and is flattened for search, because a reader looking
 * for a page names it rather than its path. The models tab lists what the
 * console generates from retained memories, which is a different endpoint from
 * the page tree.
 */
export function KnowledgePanel({
  bankId,
  subTab,
}: {
  bankId: string;
  subTab: KnowledgeSubTab;
}) {
  const t = useTranslations("panels.knowledge");
  const [search, setSearch] = useState("");

  const tree = useAsyncData(() => knowledgeBaseApi.tree(bankId), [bankId]);

  if (subTab === "models") {
    return <MentalModelsView bankId={bankId} />;
  }

  const nodes = readItems<KnowledgeNode>(tree.data, "nodes");
  const flat = flatten(nodes);
  const filtered = search
    ? flat.filter((node) =>
        (node.title ?? node.name ?? "").toLowerCase().includes(search.toLowerCase()),
      )
    : flat;

  return (
    <div>
      <PanelHeader
        title={t("pagesTitle")}
        description={t("pagesDescription", { total: flat.length })}
        action={
          <Input
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder={t("searchPlaceholder")}
            className="max-w-[240px]"
          />
        }
      />
      <PanelState
        loading={tree.loading}
        error={tree.error}
        empty={filtered.length === 0}
        onRetry={tree.reload}
      >
        <ul className="divide-y divide-border rounded-[16px] border border-border bg-card">
          {filtered.map((node, index) => (
            <li key={node.id ?? index} className="px-4 py-2.5 text-[13px]">
              {node.title ?? node.name ?? t("untitled")}
            </li>
          ))}
        </ul>
      </PanelState>
    </div>
  );
}
