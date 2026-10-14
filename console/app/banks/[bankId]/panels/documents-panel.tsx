"use client";

import { useState } from "react";

import { documentsApi } from "../../../../lib/client/endpoints";
import { readItems, readNumber, useAsyncData } from "../../../../lib/use-async-data";
import { formatRelativeTime } from "../../../../lib/relative-time";
import { useTranslations } from "../../../../i18n";
import { Button } from "../../../../components/ui/button";
import { Input } from "../../../../components/ui/input";
import { Pagination } from "../../../../components/ui/pagination";
import { TagList } from "../../../../components/ui/facet-chip";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "../../../../components/ui/dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../../../../components/ui/table";
import { PanelHeader, PanelState } from "./panel-state";
import { DocumentChunks } from "./document-chunks";

const PAGE_SIZE = 25;

type DocumentRow = {
  id?: string;
  document_id?: string;
  filename?: string;
  created_at?: string;
  tags?: string[];
};

/**
 * Documents.
 *
 * The chunk view opens in a dialog rather than a route because it is a
 * drill-down from one row, not a place a reader would link to or navigate back
 * into.
 */
export function DocumentsPanel({ bankId }: { bankId: string }) {
  const t = useTranslations("panels.documents");
  const [page, setPage] = useState(1);
  const [search, setSearch] = useState("");
  const [openDocumentId, setOpenDocumentId] = useState<string | null>(null);

  const state = useAsyncData(() => documentsApi.list(bankId), [bankId, search]);

  const rows = readItems<DocumentRow>(state.data, "documents");
  const filtered = rows.filter((row) => {
    const name = row.filename ?? row.document_id ?? row.id ?? "";
    return !search || name.toLowerCase().includes(search.toLowerCase());
  });
  const current = filtered.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE);

  return (
    <div>
      <PanelHeader
        title={t("title")}
        description={t("description", { total: readNumber(state.data, "total", rows.length) })}
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
              <TableHead>{t("columns.document")}</TableHead>
              <TableHead>{t("columns.tags")}</TableHead>
              <TableHead>{t("columns.created")}</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {current.map((row, index) => {
              const id = row.document_id ?? row.id;
              return (
                <TableRow key={id ?? index}>
                  <TableCell className="max-w-[380px]">
                    <div className="flex flex-col">
                      <span className="truncate text-[13px] font-medium">
                        {row.filename ?? t("untitled")}
                      </span>
                      {id && (
                        <span className="truncate font-mono text-[11px] text-muted-foreground">
                          {id}
                        </span>
                      )}
                    </div>
                  </TableCell>
                  <TableCell>
                    <TagList tags={(row.tags ?? []).slice(0, 3)} size="xs" />
                  </TableCell>
                  <TableCell className="whitespace-nowrap text-[12px] text-muted-foreground">
                    {row.created_at ? formatRelativeTime(row.created_at) : "—"}
                  </TableCell>
                  <TableCell className="text-right">
                    {id && (
                      <Button size="sm" variant="outline" onClick={() => setOpenDocumentId(id)}>
                        {t("viewChunks")}
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
        <Pagination
          page={page}
          pageSize={PAGE_SIZE}
          total={filtered.length}
          onPageChange={setPage}
        />
      </PanelState>

      <Dialog
        open={openDocumentId !== null}
        onOpenChange={(open) => {
          if (!open) setOpenDocumentId(null);
        }}
      >
        <DialogContent className="max-h-[85vh] max-w-3xl overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{t("chunksTitle")}</DialogTitle>
          </DialogHeader>
          {openDocumentId && (
            <DocumentChunks bankId={bankId} documentId={openDocumentId} />
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}
