"use client";

import { useEffect } from "react";
import { BankSelector } from "@/components/bank-selector";
import { BanksOverview } from "@/components/banks-overview";
import { useBank } from "@/lib/bank-context";

export default function DashboardPage() {
  const { setCurrentBank } = useBank();

  // This page is the "all banks" view, so nothing is selected while it is open.
  // The selection survives navigating back here from a bank page otherwise, and the
  // header would keep offering bank-scoped actions for a bank that is not on screen.
  useEffect(() => {
    setCurrentBank(null);
  }, [setCurrentBank]);

  return (
    <div className="relative flex min-h-screen flex-col bg-background">
      {/* Ambient bloom behind the header. Fixed and pointer-transparent so it
          never scrolls, never intercepts clicks, and never tints the panels
          that sit on top of it. */}
      <div
        aria-hidden
        className="texture-aurora animate-aurora-drift pointer-events-none fixed inset-x-0 top-0 h-[420px] opacity-70"
      />
      <div className="relative flex flex-1 flex-col">
        <BankSelector />
        <BanksOverview />
      </div>
    </div>
  );
}