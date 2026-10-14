"use client";

import { formatRelativeTime } from "../../../../lib/relative-time";
import { useTranslations } from "../../../../i18n";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../../../../components/ui/table";

type Bucket = {
  time?: string;
  statuses?: Record<string, number>;
  total?: number;
  tokens?: {
    input_tokens?: number;
    output_tokens?: number;
    cached_tokens?: number;
    thoughts_tokens?: number;
    total_tokens?: number;
  };
};

type StatsPayload = {
  period?: string;
  trunc?: string;
  start?: string;
  buckets?: Bucket[];
};

/**
 * Model-request histogram.
 *
 * One row per time bucket, because the reader is looking for when calls
 * clustered and how they were spent, not for a single total. The status column
 * shows only the statuses a bucket actually carries: a zero would read as
 * "nothing failed" where the truth is "nothing was recorded".
 */
export function LlmRequestStats({ payload }: { payload: unknown }) {
  const t = useTranslations("panels.profile");
  const data = (payload ?? {}) as StatsPayload;
  const buckets = Array.isArray(data.buckets) ? data.buckets : [];

  if (buckets.length === 0) {
    return <p className="text-[13px] text-muted-foreground">{t("noLlmRequests")}</p>;
  }

  return (
    <div>
      {(data.period || data.trunc) && (
        <p className="mb-2 text-[12px] text-muted-foreground">
          {t("llmPeriod", {
            period: data.period ?? t("unset"),
            trunc: data.trunc ?? t("unset"),
          })}
        </p>
      )}
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t("columns.bucket")}</TableHead>
            <TableHead>{t("columns.requests")}</TableHead>
            <TableHead>{t("columns.statuses")}</TableHead>
            <TableHead>{t("columns.tokens")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {buckets.map((bucket, index) => {
            const statuses = Object.entries(bucket.statuses ?? {}).filter(
              ([, count]) => count > 0,
            );
            const tokens = bucket.tokens?.total_tokens ?? 0;
            return (
              <TableRow key={bucket.time ?? index}>
                <TableCell className="whitespace-nowrap text-[12px] text-muted-foreground">
                  {bucket.time ? formatRelativeTime(bucket.time) : "—"}
                </TableCell>
                <TableCell className="text-[13px] tabular-nums">{bucket.total ?? 0}</TableCell>
                <TableCell>
                  {statuses.length === 0 ? (
                    <span className="text-[12px] text-muted-foreground">—</span>
                  ) : (
                    <div className="flex flex-wrap gap-x-2 gap-y-0.5">
                      {statuses.map(([status, count]) => (
                        <span key={status} className="font-mono text-[11px]">
                          <span
                            className={
                              status.startsWith("2")
                                ? "text-muted-foreground"
                                : "text-destructive"
                            }
                          >
                            {status}
                          </span>{" "}
                          {count}
                        </span>
                      ))}
                    </div>
                  )}
                </TableCell>
                <TableCell
                  className="text-[12px] tabular-nums text-muted-foreground"
                  title={t("tokensBreakdown", {
                    input: bucket.tokens?.input_tokens ?? 0,
                    output: bucket.tokens?.output_tokens ?? 0,
                    cached: bucket.tokens?.cached_tokens ?? 0,
                  })}
                >
                  {tokens}
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}