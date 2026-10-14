"use client";

import { useState } from "react";
import { RefreshCw } from "lucide-react";

import { documentsApi } from "../../../../lib/client/endpoints";
import { readItems, readNumber, useAsyncData } from "../../../../lib/use-async-data";
import { useTranslations } from "../../../../i18n";
import { Button } from "../../../../components/ui/button";
import { Pagination } from "../../../../components/ui/pagination";
import { Spinner } from "../../../../components/ui/spinner";
import { PanelState } from "./panel-state";

const CHUNK_PAGE_SIZE = 10;

type ChunkRow = {
  chunk_id?: string;
  chunk_index?: number;
  chunk_text?: string;
  created_at?: string;
};

/**
 * One document's chunks, with paging and a reprocess action.
 *
 * Chunks are listed a page at a time because a document can carry hundreds of
 * them and the reader is usually looking for one; the index is shown next to
 * each so a chunk can be cited by position rather than by its opaque id.
 */
export function DocumentChunks({
  bankId,
  documentId,
}: {
  bankId: string;
  documentId: string;
}) {
  const t = useTranslations("panels.documents");
  const [page, setPage] = useState(1);
  const [reprocessing, setReprocessing] = useState(false);
  const [reprocessed, setReprocessed] = useState(false);
  const [error, setError] = useState(false);

  const offset = (page - 1) * CHUNK_PAGE_SIZE;
  const chunks = useAsyncData(
    () => documentsApi.chunks(documentId, bankId, { limit: CHUNK_PAGE_SIZE, offset }),
    [bankId, documentId, offset],
  );

  const rows = readItems<ChunkRow>(chunks.data, "items");
  const total = readNumber(chunks.data, "total", rows.length);

  async function reprocess() {
    setReprocessing(true);
    setError(false);
    setReprocessed(false);
    try {
      await documentsApi.reprocess(bankId, documentId);
      setReprocessed(true);
      chunks.reload();
    } catch {
      setError(true);
    } finally {
      setReprocessing(false);
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-[12px] text-muted-foreground">
          {t("chunkCount", { total })}
        </p>
        <div className="flex items-center gap-2">
          {reprocessed && (
            <span className="text-[12px] text-muted-foreground">{t("reprocessed")}</span>
          )}
          {error && (
            <span role="alert" className="text-[12px] text-destructive">
              {t("reprocessFailed")}
            </span>
          )}
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={reprocess}
            disabled={reprocessing}
          >
            {reprocessing ? <Spinner /> : <RefreshCw className="h-3.5 w-3.5" />}
            {t("reprocess")}
          </Button>
        </div>
      </div>

      <PanelState
        loading={chunks.loading}
        error={chunks.error}
        empty={rows.length === 0}
        emptyLabel={t("noChunks")}
        onRetry={chunks.reload}
      >
        <ol className="flex flex-col gap-2">
          {rows.map((row, index) => (
            <li
              key={row.chunk_id ?? index}
              className="rounded-[12px] border border-border bg-card px-3 py-2.5"
            >
              <div className="mb-1 flex items-center justify-between gap-3">
                <span className="font-mono text-[11px] text-muted-foreground">
                  #{row.chunk_index ?? offset + index}
                </span>
                {row.chunk_id && (
                  <span className="truncate font-mono text-[10px] text-muted-foreground">
                    {row.chunk_id}
                  </span>
                )}
              </div>
              <p className="whitespace-pre-wrap break-words text-[13px] leading-relaxed">
                {row.chunk_text ?? ""}
              </p>
            </li>
          ))}
        </ol>
        <Pagination
          page={page}
          pageSize={CHUNK_PAGE_SIZE}
          total={total}
          onPageChange={setPage}
        />
      </PanelState>
    </div>
  );
}