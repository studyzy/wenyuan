"use client";

import type { ReactNode } from "react";

import { BankSelector } from "./bank-selector";
import { LanguageSwitcher } from "./language-switcher";

/**
 * The application header.
 *
 * It carries the bank switcher and the locale control, both of which are
 * reachable from every page, and leaves the body to whatever the page renders.
 */
export function AppHeader({ children }: { children?: ReactNode }) {
  return (
    <header className="flex h-14 shrink-0 items-center gap-3 border-b border-border bg-card px-4">
      <BankSelector />
      <div className="ml-auto flex items-center gap-3">
        {children}
        <LanguageSwitcher />
      </div>
    </header>
  );
}

/** Page scaffold: a header above a scrolling body. */
export function AppShell({ children }: { children: ReactNode }) {
  return (
    <div className="flex min-h-screen flex-col bg-surface-page">
      <AppHeader />
      <div className="flex flex-1 flex-col overflow-hidden">{children}</div>
    </div>
  );
}