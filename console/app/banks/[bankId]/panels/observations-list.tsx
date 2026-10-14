"use client";

import { useTranslations } from "../../../../i18n";
import { TagList } from "../../../../components/ui/facet-chip";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../../../../components/ui/table";
import { formatRelativeTime } from "../../../../lib/relative-time";

type ObservationRow = {
  id?: string;
  text?: string;
  proof_count?: number;
  tags?: string[];
  date?: string;
};

/**
 * Consolidated observations.
 *
 * Ordered as the route returns them, which is newest first. The proof count is
 * shown because it is what distinguishes a conclusion drawn from many facts
 * from one drawn from a single mention — the evidence strength that recency
 * alone does not convey.
 */
export function ObservationsList({ payload }: { payload: unknown }) {
  const t = useTranslations("panels.profile");
  const rows = extractObservations(payload);

  if (rows.length === 0) {
    return <p className="text-[13px] text-muted-foreground">{t("noObservations")}</p>;
  }

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{t("columns.observation")}</TableHead>
          <TableHead>{t("columns.proofCount")}</TableHead>
          <TableHead>{t("columns.tags")}</TableHead>
          <TableHead>{t("columns.date")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((row, index) => (
          <TableRow key={row.id ?? index}>
            <TableCell className="max-w-[420px]">
              <span className="whitespace-pre-wrap break-words text-[13px] leading-relaxed">
                {row.text ?? ""}
              </span>
            </TableCell>
            <TableCell className="text-[13px] tabular-nums">{row.proof_count ?? 0}</TableCell>
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
  );
}

/**
 * Read the observation rows from either envelope shape the route may return:
 * a bare array, or an object carrying them under items.
 */
function extractObservations(payload: unknown): ObservationRow[] {
  if (Array.isArray(payload)) return payload as ObservationRow[];
  if (payload && typeof payload === "object") {
    const items = (payload as { items?: unknown }).items;
    if (Array.isArray(items)) return items as ObservationRow[];
  }
  return [];
}