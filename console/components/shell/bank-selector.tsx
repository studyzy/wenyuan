"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { ChevronDown, Plus, Search } from "lucide-react";

import { banksApi } from "../../lib/client/endpoints";
import { BANKS_PAGE_SIZE, useBank } from "../../lib/bank-context";
import { bankRoute } from "../../lib/bank-url";
import { useTranslations } from "../../i18n";
import { cn } from "../../lib/utils";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Spinner } from "../ui/spinner";

/**
 * The header control that switches the active bank.
 *
 * The list is paged because an installation can hold far more banks than fit in
 * a menu: the popover loads the first page on open and appends when the list is
 * scrolled to the end. Typing searches server-side rather than filtering what is
 * already loaded, otherwise a bank past the first page would be unreachable by
 * name.
 */
export function BankSelector() {
  const t = useTranslations("nav.bank");
  const router = useRouter();
  const {
    currentBank,
    currentBankName,
    bankInfos,
    banksLoading,
    banksLoadingMore,
    hasMoreBanks,
    totalBanks,
    searchBanks,
    loadMoreBanks,
  } = useBank();

  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [creating, setCreating] = useState(false);
  const [newBankId, setNewBankId] = useState("");
  const listRef = useRef<HTMLUListElement>(null);

  // Debounce so a fast typist does not issue a request per keystroke.
  useEffect(() => {
    if (!open) return;
    const handle = setTimeout(() => void searchBanks(query), 200);
    return () => clearTimeout(handle);
  }, [query, open, searchBanks]);

  const selectBank = (bankId: string) => {
    setOpen(false);
    setQuery("");
    router.push(bankRoute(bankId));
  };

  const createBank = async () => {
    const bankId = newBankId.trim();
    if (!bankId) return;
    try {
      await banksApi.create({ bank_id: bankId });
      setNewBankId("");
      setCreating(false);
      setOpen(false);
      await searchBanks("");
      router.push(bankRoute(bankId));
    } catch {
      // A failed create leaves the form open so the operator can correct it.
    }
  };

  const label = currentBankName ?? currentBank ?? t("select");

  return (
    <div className="relative">
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        aria-haspopup="listbox"
        aria-expanded={open}
        className="flex h-9 max-w-[280px] items-center gap-2 rounded-[10px] border border-border bg-card px-3 text-[13px] font-medium transition-colors hover:border-primary/40"
      >
        <span className="truncate">{label}</span>
        <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" />
      </button>

      {open && (
        <div className="absolute left-0 top-11 z-50 w-[360px] rounded-[10px] border border-border bg-popover p-3 shadow-[0_8px_24px_rgba(0,0,0,0.12)]">
          <div className="relative mb-2">
            <Search className="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
            <Input
              autoFocus
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t("searchPlaceholder")}
              className="pl-8"
            />
          </div>

          {banksLoading ? (
            <div className="flex justify-center py-6">
              <Spinner />
            </div>
          ) : bankInfos.length === 0 ? (
            <p className="px-1 py-6 text-center text-[13px] text-muted-foreground">
              {t("none")}
            </p>
          ) : (
            <ul
              ref={listRef}
              role="listbox"
              onScroll={(event) => {
                const element = event.currentTarget;
                const atEnd =
                  element.scrollTop + element.clientHeight >= element.scrollHeight - 24;
                if (atEnd && hasMoreBanks) void loadMoreBanks();
              }}
              className="max-h-[280px] overflow-y-auto"
            >
              {bankInfos.map((bank) => {
                const display = bank.display_alias ?? bank.name ?? bank.bank_id;
                return (
                  <li key={bank.bank_id}>
                    <button
                      type="button"
                      role="option"
                      aria-selected={bank.bank_id === currentBank}
                      onClick={() => selectBank(bank.bank_id)}
                      className={cn(
                        "flex w-full flex-col items-start rounded-md px-2 py-1.5 text-left transition-colors hover:bg-accent",
                        bank.bank_id === currentBank && "bg-accent",
                      )}
                    >
                      <span className="truncate text-[13px] font-medium">{display}</span>
                      <span className="truncate text-[11px] text-muted-foreground">
                        {bank.bank_id} · {bank.fact_count} {t("facts")}
                      </span>
                    </button>
                  </li>
                );
              })}
              {banksLoadingMore && (
                <li className="flex justify-center py-2">
                  <Spinner size="sm" />
                </li>
              )}
              <li className="px-2 py-1 text-center text-[11px] text-muted-foreground">
                {bankInfos.length} / {Math.max(totalBanks, bankInfos.length)}
              </li>
            </ul>
          )}

          <div className="mt-2 border-t border-border pt-2">
            {creating ? (
              <div className="flex items-center gap-2">
                <Input
                  autoFocus
                  value={newBankId}
                  onChange={(event) => setNewBankId(event.target.value)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter") void createBank();
                    if (event.key === "Escape") setCreating(false);
                  }}
                  placeholder={t("newBankId")}
                />
                <Button size="sm" variant="gradient" onClick={() => void createBank()}>
                  {t("create")}
                </Button>
              </div>
            ) : (
              <Button
                size="sm"
                variant="ghost"
                className="w-full justify-start gap-2"
                onClick={() => setCreating(true)}
              >
                <Plus className="h-4 w-4" />
                {t("create")}
              </Button>
            )}
          </div>
        </div>
      )}
    </div>
  );
}