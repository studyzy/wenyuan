"use client";

import { useCallback } from "react";
import { useRouter } from "next/navigation";

import {
  CONFIGURATION_SUBTABS,
  DATA_SUBTABS,
  KNOWLEDGE_SUBTABS,
  VIEWS,
  bankDetailPath,
  nextViewSearch,
  type BankViewState,
} from "../../../lib/bank-detail";
import { useTranslations } from "../../../i18n";
import { cn } from "../../../lib/utils";
import { BankHomePanel } from "./panels/bank-home-panel";
import { BankProfilePanel } from "./panels/bank-profile-panel";
import { DataPanel } from "./panels/data-panel";
import { DocumentsPanel } from "./panels/documents-panel";
import { EntitiesPanel } from "./panels/entities-panel";
import { KnowledgePanel } from "./panels/knowledge-panel";
import { RecallPanel } from "./panels/recall-panel";
import { ReflectPanel } from "./panels/reflect-panel";

type Patch = Partial<Record<"view" | "data" | "knowledge" | "config", string>>;

/**
 * Bank detail shell.
 *
 * Holds the view strip and the sub-tab strip, and renders the panel the current
 * view names. Switching writes query parameters rather than local state, so the
 * address bar always describes what is on screen and any view can be linked to.
 */
export function BankDetailView({
  bankId,
  initialState,
}: {
  bankId: string;
  initialState: BankViewState;
}) {
  const router = useRouter();
  const t = useTranslations("panels.views");

  const push = useCallback(
    (patch: Patch) => {
      const current = new URLSearchParams(window.location.search);
      router.push(`${bankDetailPath(bankId)}${nextViewSearch(current, patch)}`);
    },
    [bankId, router],
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <nav
        aria-label={t("sectionLabel")}
        className="flex shrink-0 gap-1 overflow-x-auto border-b border-border px-4"
      >
        {VIEWS.map((view) => (
          <SubTab
            key={view}
            label={t(view)}
            active={initialState.view === view}
            onClick={() => push({ view })}
          />
        ))}
      </nav>

      {initialState.view === "data" && (
        <SubTabStrip
          label={t("dataSubLabel")}
          items={DATA_SUBTABS}
          active={initialState.dataSubTab}
          labelFor={(tab) => t(`subData${tab.charAt(0).toUpperCase()}${tab.slice(1).replace(/-([a-z])/g, (_, c) => c.toUpperCase())}`)}
          onSelect={(tab) => push({ data: tab })}
        />
      )}
      {initialState.view === "knowledge" && (
        <SubTabStrip
          label={t("knowledgeSubLabel")}
          items={KNOWLEDGE_SUBTABS}
          active={initialState.knowledgeSubTab}
          labelFor={(tab) => t(`subKnowledge${tab.charAt(0).toUpperCase()}${tab.slice(1)}`)}
          onSelect={(tab) => push({ knowledge: tab })}
        />
      )}
      {initialState.view === "profile" && (
        <SubTabStrip
          label={t("profileSubLabel")}
          items={CONFIGURATION_SUBTABS}
          active={initialState.configurationSubTab}
          labelFor={(tab) => t(`subProfile${tab.charAt(0).toUpperCase()}${tab.slice(1).replace(/-([a-z])/g, (_, c) => c.toUpperCase())}`)}
          onSelect={(tab) => push({ config: tab })}
        />
      )}

      <div className="min-h-0 flex-1 overflow-y-auto p-6">
        {initialState.view === "home" && <BankHomePanel bankId={bankId} />}
        {initialState.view === "recall" && <RecallPanel bankId={bankId} />}
        {initialState.view === "reflect" && <ReflectPanel bankId={bankId} />}
        {initialState.view === "data" && (
          <DataPanel bankId={bankId} subTab={initialState.dataSubTab} />
        )}
        {initialState.view === "documents" && <DocumentsPanel bankId={bankId} />}
        {initialState.view === "entities" && <EntitiesPanel bankId={bankId} />}
        {initialState.view === "knowledge" && (
          <KnowledgePanel bankId={bankId} subTab={initialState.knowledgeSubTab} />
        )}
        {initialState.view === "profile" && (
          <BankProfilePanel bankId={bankId} subTab={initialState.configurationSubTab} />
        )}
      </div>
    </div>
  );
}

function SubTab({
  label,
  active,
  onClick,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-current={active ? "page" : undefined}
      className={cn(
        "shrink-0 cursor-pointer border-b-2 px-3 py-2.5 text-[13px] font-medium transition-colors",
        active
          ? "border-primary text-foreground"
          : "border-transparent text-muted-foreground hover:text-foreground",
      )}
    >
      {label}
    </button>
  );
}

function SubTabStrip<T extends string>({
  label,
  items,
  active,
  labelFor,
  onSelect,
}: {
  label: string;
  items: readonly T[];
  active: T;
  labelFor: (item: T) => string;
  onSelect: (item: T) => void;
}) {
  return (
    <nav
      aria-label={label}
      className="flex shrink-0 flex-wrap gap-1 border-b border-border bg-muted/30 px-4 py-2"
    >
      {items.map((item) => (
        <button
          key={item}
          type="button"
          onClick={() => onSelect(item)}
          aria-current={active === item ? "page" : undefined}
          className={cn(
            "cursor-pointer rounded-full px-3 py-1 text-[12px] font-medium transition-colors",
            active === item
              ? "bg-card text-foreground shadow-sm"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          {labelFor(item)}
        </button>
      ))}
    </nav>
  );
}