"use client";

import { useMemo, useState } from "react";

import { entitiesApi } from "../../../../lib/client/endpoints";
import { Constellation } from "../../../../components/graph/constellation";
import { convertHindsightGraphData } from "../../../../components/graph/graph-data";
import type { GraphData, UpstreamGraphPayload } from "../../../../components/graph/graph-data";
import { readItems, readNumber, useAsyncData } from "../../../../lib/use-async-data";
import { useTranslations } from "../../../../i18n";
import { Input } from "../../../../components/ui/input";
import { Pagination } from "../../../../components/ui/pagination";
import { Card, CardContent, CardHeader, CardTitle } from "../../../../components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../../../../components/ui/table";
import { PanelHeader, PanelState } from "./panel-state";

const PAGE_SIZE = 25;

type EntityRow = { id?: string; name?: string; entity?: string; mention_count?: number };

/**
 * Entities and the graph they form.
 *
 * The list is the primary surface because it is what can be paged and searched;
 * the graph summary is presented beside it as counts, since a full interactive
 * graph view belongs to its own component rather than being improvised here.
 */
export function EntitiesPanel({ bankId }: { bankId: string }) {
  const t = useTranslations("panels.entities");
  const [page, setPage] = useState(1);
  const [search, setSearch] = useState("");

  const list = useAsyncData(() => entitiesApi.list(bankId), [bankId, search]);
  const graph = useAsyncData(() => entitiesApi.graph(bankId), [bankId]);

  const rows = readItems<EntityRow>(list.data);
  const filtered = rows.filter((row) => {
    const name = row.name ?? row.entity ?? "";
    return !search || name.toLowerCase().includes(search.toLowerCase());
  });
  const current = filtered.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE);

  const graphPayload = graph.data as
    | ({ total_entities?: number; total_edges?: number } & Record<string, unknown>)
    | null;

  // The graph endpoint returns its own shape; convert once per payload rather
  // than on every render.
  const graphData: GraphData | null = useMemo(() => {
    if (!graphPayload) return null;
    const converted = convertHindsightGraphData(graphPayload as UpstreamGraphPayload);
    return converted.nodes.length > 0 ? converted : null;
  }, [graphPayload]);

  return (
    <div>
      <PanelHeader
        title={t("title")}
        description={t("description", { total: readNumber(list.data, "total", rows.length) })}
        action={
          <Input
            value={search}
            onChange={(event) => {
              setSearch(event.target.value);
              setPage(1);
            }}
            placeholder={t("searchPlaceholder")}
            className="max-w-[240px]"
          />
        }
      />

      <div className="mb-6 grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Card>
          <CardHeader className="p-4 pb-1">
            <CardTitle className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
              {t("graphEntities")}
            </CardTitle>
          </CardHeader>
          <CardContent className="p-4 pt-0">
            <span className="text-[24px] font-semibold tabular-nums">
              {graph.loading || graph.error ? "—" : (graphPayload?.total_entities ?? 0)}
            </span>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="p-4 pb-1">
            <CardTitle className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
              {t("graphEdges")}
            </CardTitle>
          </CardHeader>
          <CardContent className="p-4 pt-0">
            <span className="text-[24px] font-semibold tabular-nums">
              {graph.loading || graph.error ? "—" : (graphPayload?.total_edges ?? 0)}
            </span>
          </CardContent>
        </Card>
      </div>

      {graphData && (
        <div className="mb-6">
          <h3 className="mb-2 text-[13px] font-semibold">{t("graphTitle")}</h3>
          <Constellation data={graphData} height={420} compactLabels />
        </div>
      )}

      <PanelState
        loading={list.loading}
        error={list.error}
        empty={filtered.length === 0}
        onRetry={list.reload}
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t("columns.entity")}</TableHead>
              <TableHead className="text-right">{t("columns.mentions")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {current.map((row, index) => (
              <TableRow key={row.id ?? index}>
                <TableCell className="text-[13px]">{row.name ?? row.entity ?? "—"}</TableCell>
                <TableCell className="text-right tabular-nums text-[13px]">
                  {row.mention_count ?? "—"}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        <Pagination
          page={page}
          pageSize={PAGE_SIZE}
          total={filtered.length}
          onPageChange={setPage}
        />
      </PanelState>
    </div>
  );
}
