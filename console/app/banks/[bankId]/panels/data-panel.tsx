"use client";

import { useState } from "react";

import { memoriesApi } from "../../../../lib/client/endpoints";
import { readItems, readNumber, useAsyncData } from "../../../../lib/use-async-data";
import { formatRelativeTime } from "../../../../lib/relative-time";
import { useTranslations } from "../../../../i18n";
import { Input } from "../../../../components/ui/input";
import { Pagination } from "../../../../components/ui/pagination";
import { EntityChip, TagList } from "../../../../components/ui/facet-chip";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../../../../components/ui/table";
import { PanelHeader, PanelState } from "./panel-state";
import { MemoryDetailModal } from "../../../../components/memory/memory-detail-modal";
import type { DataSubTab } from "../../../../lib/bank-detail";

const PAGE_SIZE = 25;

type MemoryRow = {
  id?: string;
  text?: string;
  type?: string;
  date?: string;
  entities?: string[];
  tags?: string[];
};

/**
 * Memories, split by fact type.
 *
 * The sub-tab is the fact type, so switching it is a filter on the same list
 * rather than a different view. Text is truncated rather than wrapped because a
 * retained document can be arbitrarily long and one row must stay one row.
 */
export function DataPanel({ bankId, subTab }: { bankId: string; subTab: DataSubTab }) {
  const t = useTranslations("panels.data");
  const [page, setPage] = useState(1);
  const [search, setSearch] = useState("");
  const [openMemoryId, setOpenMemoryId] = useState<string | null>(null);

  const state = useAsyncData(
    () => memoriesApi.list(bankId),
    [bankId, subTab, search],
  );

  const all = readItems<MemoryRow>(state.data);
  const filtered = all.filter((row) => {
    const matchesType = !row.type || row.type === subTab;
    const matchesSearch =
      !search || (row.text ?? "").toLowerCase().includes(search.toLowerCase());
    return matchesType && matchesSearch;
  });
  const current = filtered.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE);

  return (
    <div>
      <PanelHeader
        title={t("title")}
        description={t("description", { total: readNumber(state.data, "total", all.length) })}
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
      <PanelState
        loading={state.loading}
        error={state.error}
        empty={filtered.length === 0}
        onRetry={state.reload}
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t("columns.text")}</TableHead>
              <TableHead>{t("columns.entities")}</TableHead>
              <TableHead>{t("columns.tags")}</TableHead>
              <TableHead>{t("columns.date")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {current.map((row, index) => (
              <TableRow
                key={row.id ?? index}
                className={row.id ? "cursor-pointer" : undefined}
                onClick={() => {
                  if (row.id) setOpenMemoryId(row.id);
                }}
              >
                <TableCell className="max-w-[420px]">
                  <p className="truncate text-[13px] text-foreground">{row.text ?? "—"}</p>
                </TableCell>
                <TableCell>
                  <div className="flex flex-wrap gap-1">
                    {(row.entities ?? []).slice(0, 3).map((entity) => (
                      <EntityChip key={entity} entity={entity} size="xs" />
                    ))}
                  </div>
                </TableCell>
                <TableCell>
                  <TagList tags={(row.tags ?? []).slice(0, 3)} size="xs" />
                </TableCell>
                <TableCell className="whitespace-nowrap text-[12px] text-muted-foreground">
                  {row.date ? formatRelativeTime(row.date) : "—"}
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

      <MemoryDetailModal
        memoryId={openMemoryId}
        bankId={bankId}
        onClose={() => setOpenMemoryId(null)}
        onChanged={state.reload}
      />
    </div>
  );
}
