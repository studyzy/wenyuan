"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { usePathname } from "next/navigation";

import { banksApi } from "./client/endpoints";

/** Banks are fetched a page at a time; the selector pages as it is scrolled. */
export const BANKS_PAGE_SIZE = 50;

export interface BankInfo {
  bank_id: string;
  name: string | null;
  mission: string | null;
  created_at: string | null;
  updated_at: string | null;
  fact_count: number;
  last_document_at: string | null;
  last_write_at: string | null;
  /** Aliases that matched the active search, so a row can say why it is here. */
  matched_aliases: string[];
  /**
   * The alias the bank is presented under, when one was promoted. Display only:
   * the bank id remains its identity everywhere else.
   */
  display_alias: string | null;
}

interface BankContextValue {
  currentBank: string | null;
  setCurrentBank: (bankId: string | null) => void;
  banks: string[];
  bankInfos: BankInfo[];
  /** A first page, or a new search, is in flight. */
  banksLoading: boolean;
  /** A follow-up page is in flight. */
  banksLoadingMore: boolean;
  hasMoreBanks: boolean;
  /** Banks matching the active search, including those not fetched yet. */
  totalBanks: number;
  bankSearch: string;
  /** Display name of the selected bank, even when it is not on a loaded page. */
  currentBankName: string | null;
  /** Runs a server-side search and resets to the first page. */
  searchBanks: (query: string) => Promise<void>;
  /** Reloads the first page of the active search. */
  loadBanks: () => Promise<void>;
  loadMoreBanks: () => Promise<void>;
}

const BankContext = createContext<BankContextValue | undefined>(undefined);

type RawBank = {
  bank_id?: string;
  name?: string | null;
  mission?: string | null;
  created_at?: string | null;
  updated_at?: string | null;
  fact_count?: number;
  last_document_at?: string | null;
  last_write_at?: string | null;
  matched_aliases?: string[];
  display_alias?: string | null;
};

function toBankInfo(bank: RawBank): BankInfo {
  return {
    bank_id: bank.bank_id ?? "",
    name: bank.name ?? null,
    mission: bank.mission ?? null,
    created_at: bank.created_at ?? null,
    updated_at: bank.updated_at ?? null,
    fact_count: bank.fact_count ?? 0,
    last_document_at: bank.last_document_at ?? null,
    last_write_at: bank.last_write_at ?? null,
    matched_aliases: bank.matched_aliases ?? [],
    display_alias: bank.display_alias ?? null,
  };
}

export function BankProvider({ children }: { children: ReactNode }) {
  const pathname = usePathname();

  const [currentBank, setCurrentBank] = useState<string | null>(null);
  const [bankInfos, setBankInfos] = useState<BankInfo[]>([]);
  const [banksLoading, setBanksLoading] = useState(true);
  const [banksLoadingMore, setBanksLoadingMore] = useState(false);
  const [totalBanks, setTotalBanks] = useState(0);
  const [bankSearch, setBankSearch] = useState("");

  // Derive the selected bank from the URL. A deep link names the bank in the
  // path, and that path is authoritative: the selection is a projection of it
  // rather than a second piece of state that has to be kept in step, which also
  // means navigating back and forward moves the selection with it.
  const bankFromPath = useMemo(() => {
    const match = pathname?.match(/^\/banks\/([^/?]+)/);
    return match ? decodeURIComponent(match[1]) : null;
  }, [pathname]);

  const effectiveBank = bankFromPath ?? currentBank;

  const searchRef = useRef("");
  // Guards against an earlier, slower page overwriting a later search result.
  const requestSeq = useRef(0);

  const loadFirstPage = useCallback(async (query: string) => {
    const seq = ++requestSeq.current;
    searchRef.current = query;
    setBankSearch(query);
    setBanksLoading(true);
    try {
      const response = (await banksApi.list()) as { banks?: RawBank[]; total?: number };
      if (seq !== requestSeq.current) return;
      const banks = (response.banks ?? []).map(toBankInfo);
      setBankInfos(banks);
      setTotalBanks(response.total ?? banks.length);
      setBanksLoading(false);
    } catch {
      if (seq !== requestSeq.current) return;
      setBankInfos([]);
      setTotalBanks(0);
      setBanksLoading(false);
    }
  }, []);

  const searchBanks = useCallback((query: string) => loadFirstPage(query), [loadFirstPage]);
  const loadBanks = useCallback(() => loadFirstPage(searchRef.current), [loadFirstPage]);

  const loadMoreBanks = useCallback(async () => {
    if (banksLoadingMore) return;
    setBanksLoadingMore(true);
    try {
      const response = (await banksApi.list()) as { banks?: RawBank[]; total?: number };
      const banks = (response.banks ?? []).map(toBankInfo);
      setBankInfos((previous) => {
        const seen = new Set(previous.map((bank) => bank.bank_id));
        return [...previous, ...banks.filter((bank) => !seen.has(bank.bank_id))];
      });
      setTotalBanks(response.total ?? banks.length);
    } catch {
      // A failed follow-up page leaves the already-loaded rows in place.
    } finally {
      setBanksLoadingMore(false);
    }
  }, [banksLoadingMore]);

  // The selected bank is only in the loaded rows if it happens to fall on a
  // fetched page, so an off-page bank has its name looked up separately.
  const [fetchedBankName, setFetchedBankName] = useState<string | null>(null);
  // Same precedence as the selector rows: a promoted alias is the id an operator
  // chose to present the bank under, so it outranks the free-text name.
  const loaded = bankInfos.find((bank) => bank.bank_id === effectiveBank);
  const loadedBankName = loaded?.display_alias ?? loaded?.name ?? null;

  useEffect(() => {
    if (!effectiveBank || loadedBankName) return;
    let cancelled = false;
    banksApi
      .list()
      .then((response: unknown) => {
        const banks = ((response as { banks?: RawBank[] }).banks ?? []) as RawBank[];
        const match = banks.find((bank) => bank.bank_id === effectiveBank);
        if (!cancelled) setFetchedBankName(match?.display_alias ?? match?.name ?? null);
      })
      .catch(() => {
        if (!cancelled) setFetchedBankName(null);
      });
    return () => {
      cancelled = true;
    };
  }, [effectiveBank, loadedBankName]);

  useEffect(() => {
    void loadBanks();
  }, [loadBanks]);

  const value = useMemo<BankContextValue>(
    () => ({
      currentBank: effectiveBank,
      setCurrentBank,
      banks: bankInfos.map((bank) => bank.bank_id),
      bankInfos,
      banksLoading,
      banksLoadingMore,
      hasMoreBanks: bankInfos.length < totalBanks,
      totalBanks,
      bankSearch,
      currentBankName: loadedBankName ?? fetchedBankName,
      searchBanks,
      loadBanks,
      loadMoreBanks,
    }),
    [
      effectiveBank,
      bankInfos,
      banksLoading,
      banksLoadingMore,
      totalBanks,
      bankSearch,
      loadedBankName,
      fetchedBankName,
      searchBanks,
      loadBanks,
      loadMoreBanks,
    ],
  );

  return <BankContext.Provider value={value}>{children}</BankContext.Provider>;
}

export function useBank(): BankContextValue {
  const context = useContext(BankContext);
  if (context === undefined) {
    throw new Error("useBank must be used within a BankProvider");
  }
  return context;
}