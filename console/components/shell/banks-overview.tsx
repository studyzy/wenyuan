"use client";

import { useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import { Search, Trash2, X } from "lucide-react";

import { banksApi } from "../../lib/client/endpoints";
import { useBank, type BankInfo } from "../../lib/bank-context";
import { bankRoute } from "../../lib/bank-url";
import { formatRelativeTime } from "../../lib/relative-time";
import { useTranslations } from "../../i18n";
import { Button } from "../ui/button";
import { Checkbox } from "../ui/checkbox";
import { Input } from "../ui/input";
import { Pagination } from "../ui/pagination";
import { Spinner } from "../ui/spinner";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "../ui/alert-dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../ui/table";

const PAGE_SIZE = 25;

function formatCompact(value: number): string {
  if (value < 1000) return String(value);
  if (value < 1_000_000) return `${(value / 1000).toFixed(1)}k`;
  return `${(value / 1_000_000).toFixed(1)}M`;
}

/** The most recent of the activity timestamps a bank reports. */
function lastActivityOf(bank: BankInfo): string | null {
  const candidates = [bank.last_document_at, bank.last_write_at, bank.updated_at].filter(
    (value): value is string => typeof value === "string" && value.length > 0,
  );
  if (candidates.length === 0) return null;
  return candidates.sort().at(-1) ?? null;
}

/**
 * The all-banks overview, which the dashboard hosts.
 *
 * Rows are the navigation: selecting one opens that bank. Deletion is confirmed
 * first because it is not recoverable from the console, and a bulk delete runs
 * one request at a time so a partial failure leaves the already-deleted rows
 * gone and the rest intact rather than reporting an all-or-nothing result it
 * cannot deliver.
 */
export function BanksOverview() {
  const t = useTranslations("dashboard");
  const router = useRouter();
  const { bankInfos, banksLoading, totalBanks, bankSearch, searchBanks, loadBanks } = useBank();

  const [query, setQuery] = useState(bankSearch);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [page, setPage] = useState(1);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);

  const pageCount = Math.max(1, Math.ceil(bankInfos.length / PAGE_SIZE));
  const current = useMemo(
    () => bankInfos.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE),
    [bankInfos, page],
  );

  const allOnPageSelected = current.length > 0 && current.every((b) => selected.has(b.bank_id));

  const toggleAllOnPage = () => {
    setSelected((previous) => {
      const next = new Set(previous);
      if (allOnPageSelected) for (const bank of current) next.delete(bank.bank_id);
      else for (const bank of current) next.add(bank.bank_id);
      return next;
    });
  };

  const toggleOne = (bankId: string) => {
    setSelected((previous) => {
      const next = new Set(previous);
      if (next.has(bankId)) next.delete(bankId);
      else next.add(bankId);
      return next;
    });
  };

  const deleteSelected = async () => {
    setBusy(true);
    const ids = [...selected];
    for (const bankId of ids) {
      try {
        await banksApi.remove(bankId);
      } catch {
        // Stop at the first failure so the operator sees exactly what remains.
        break;
      }
    }
    setSelected(new Set());
    setBusy(false);
    setConfirming(false);
    await loadBanks();
  };

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center gap-3">
        <div className="relative max-w-[360px] flex-1">
          <Search className="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
          <Input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                void searchBanks(query);
                setPage(1);
              }
            }}
            placeholder={t("searchPlaceholder")}
            className="pl-8"
          />
        </div>
        {query && (
          <Button
            variant="ghost"
            size="sm"
            className="gap-1"
            onClick={() => {
              setQuery("");
              void searchBanks("");
              setPage(1);
            }}
          >
            <X className="h-4 w-4" />
            {t("clear")}
          </Button>
        )}
        {selected.size > 0 && (
          <Button
            variant="destructive"
            size="sm"
            className="ml-auto gap-2"
            onClick={() => setConfirming(true)}
          >
            <Trash2 className="h-4 w-4" />
            {t("deleteSelected", { count: selected.size })}
          </Button>
        )}
      </div>

      {banksLoading ? (
        <div className="flex justify-center py-16">
          <Spinner size="lg" />
        </div>
      ) : bankInfos.length === 0 ? (
        <div className="rounded-[16px] border border-border bg-card px-6 py-16 text-center">
          <p className="text-sm text-muted-foreground">{t("noBanks")}</p>
        </div>
      ) : (
        <>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-10">
                  <Checkbox
                    checked={allOnPageSelected}
                    onCheckedChange={toggleAllOnPage}
                    aria-label={t("selectAll")}
                  />
                </TableHead>
                <TableHead>{t("columns.bank")}</TableHead>
                <TableHead>{t("columns.aliases")}</TableHead>
                <TableHead className="text-right">{t("columns.facts")}</TableHead>
                <TableHead>{t("columns.lastActivity")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {current.map((bank) => {
                const display = bank.display_alias ?? bank.name ?? bank.bank_id;
                const activity = lastActivityOf(bank);
                return (
                  <TableRow
                    key={bank.bank_id}
                    className="cursor-pointer"
                    onClick={() => router.push(bankRoute(bank.bank_id))}
                  >
                    <TableCell onClick={(event) => event.stopPropagation()}>
                      <Checkbox
                        checked={selected.has(bank.bank_id)}
                        onCheckedChange={() => toggleOne(bank.bank_id)}
                        aria-label={t("selectRow", { name: display })}
                      />
                    </TableCell>
                    <TableCell>
                      <div className="flex flex-col">
                        <span className="text-[13px] font-medium">{display}</span>
                        <span className="font-mono text-[11px] text-muted-foreground">
                          {bank.bank_id}
                        </span>
                      </div>
                    </TableCell>
                    <TableCell className="text-[12px] text-muted-foreground">
                      {bank.matched_aliases.length > 0 ? bank.matched_aliases.join(", ") : "—"}
                    </TableCell>
                    <TableCell className="text-right text-[13px] tabular-nums">
                      {formatCompact(bank.fact_count)}
                    </TableCell>
                    <TableCell className="text-[12px] text-muted-foreground">
                      {activity ? formatRelativeTime(activity) : "—"}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>

          <div className="flex items-center justify-between">
            <p className="text-xs text-muted-foreground">
              {t("totalBanks", { count: totalBanks })}
            </p>
            <Pagination
              page={page}
              pageSize={PAGE_SIZE}
              total={bankInfos.length}
              disabled={busy}
              onPageChange={setPage}
            />
          </div>
        </>
      )}

      <AlertDialog open={confirming} onOpenChange={setConfirming}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("confirmDelete.title")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("confirmDelete.body", { count: selected.size })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>{t("confirmDelete.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              disabled={busy}
              onClick={(event) => {
                event.preventDefault();
                void deleteSelected();
              }}
            >
              {busy ? <Spinner size="sm" /> : t("confirmDelete.confirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

export { PAGE_SIZE as BANKS_OVERVIEW_PAGE_SIZE };