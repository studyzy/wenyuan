"use client";

import { createContext, useContext, useMemo, useState, type ReactNode } from "react";

/** Bank-selection client state used by the shell and pages. */
type BankContextValue = {
  bankId: string | null;
  selectBank: (bankId: string) => void;
  clearBank: () => void;
};

const BankContext = createContext<BankContextValue | null>(null);

export function BankProvider({ children }: { children: ReactNode }) {
  const [bankId, setBankId] = useState<string | null>(null);

  const value = useMemo<BankContextValue>(
    () => ({
      bankId,
      selectBank: (next) => setBankId(next),
      clearBank: () => setBankId(null),
    }),
    [bankId],
  );

  return <BankContext.Provider value={value}>{children}</BankContext.Provider>;
}

export function useBankSelection(): BankContextValue {
  const context = useContext(BankContext);
  if (!context) {
    throw new Error("useBankSelection must be used inside BankProvider");
  }
  return context;
}