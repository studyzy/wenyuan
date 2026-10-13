"use client";

import { useRouter } from "next/navigation";
import { useCallback } from "react";
import {
  CONFIGURATION_SUBTABS,
  DATA_SUBTABS,
  KNOWLEDGE_SUBTABS,
  VIEWS,
  bankDetailPath,
  nextViewSearch,
  type BankViewState,
} from "../../../lib/bank-detail";

/**
 * Bank detail view. Switching tabs pushes query parameters without leaving the
 * bank path segment.
 */
export function BankDetailView({
  bankId,
  initialState,
}: {
  bankId: string;
  initialState: BankViewState;
}) {
  const router = useRouter();

  const push = useCallback(
    (patch: Partial<Record<"view" | "data" | "knowledge" | "config", string>>) => {
      const current = new URLSearchParams(window.location.search);
      const search = nextViewSearch(current, patch);
      router.push(`${bankDetailPath(bankId)}${search}`);
    },
    [bankId, router],
  );

  return (
    <section>
      <nav aria-label="view">
        {VIEWS.map((view) => (
          <button key={view} type="button" onClick={() => push({ view })}>
            {view}
          </button>
        ))}
      </nav>
      <div data-view={initialState.view}>
        {initialState.view === "data" ? (
          <nav aria-label="data-subtab">
            {DATA_SUBTABS.map((tab) => (
              <button key={tab} type="button" onClick={() => push({ data: tab })}>
                {tab}
              </button>
            ))}
          </nav>
        ) : null}
        {initialState.view === "knowledge" ? (
          <nav aria-label="knowledge-subtab">
            {KNOWLEDGE_SUBTABS.map((tab) => (
              <button key={tab} type="button" onClick={() => push({ knowledge: tab })}>
                {tab}
              </button>
            ))}
          </nav>
        ) : null}
        {initialState.view === "configuration" ? (
          <nav aria-label="configuration-subtab">
            {CONFIGURATION_SUBTABS.map((tab) => (
              <button key={tab} type="button" onClick={() => push({ config: tab })}>
                {tab}
              </button>
            ))}
          </nav>
        ) : null}
      </div>
    </section>
  );
}