"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import {
  ChevronLeft,
  ChevronRight,
  Database,
  FileText,
  Home,
  Network,
  Search,
  Settings,
  Sparkles,
  Users,
} from "lucide-react";

import { versionApi } from "../../lib/client/endpoints";
import { useBank } from "../../lib/bank-context";
import { bankRoute } from "../../lib/bank-url";
import { useTranslations } from "../../i18n";
import { cn } from "../../lib/utils";

export type NavItem =
  | "home"
  | "recall"
  | "reflect"
  | "data"
  | "documents"
  | "entities"
  | "knowledge"
  | "profile";

/**
 * The rail a bank's views hang off.
 *
 * It collapses to icons so a narrow window still shows the destinations, and it
 * renders nothing when no bank is selected — there is nothing to navigate to
 * yet, and an empty rail would only take width from the overview.
 *
 * The whole rail toggles on click, which is convenient but would also fire while
 * a link inside it is being clicked, so every interactive child stops
 * propagation. Keyboard activation guards on the event target for the same
 * reason: Enter on a focused link must navigate, not collapse the rail.
 */
export function Sidebar({
  currentTab,
  onTabChange,
}: {
  currentTab: NavItem;
  onTabChange: (tab: NavItem) => void;
}) {
  const t = useTranslations("nav");
  const tSidebar = useTranslations("bank.sidebar");
  const { currentBank } = useBank();
  const [collapsed, setCollapsed] = useState(true);
  const [apiVersion, setApiVersion] = useState<string | null>(null);

  useEffect(() => {
    versionApi
      .get()
      .then((response) => setApiVersion(response.version ?? null))
      .catch(() => setApiVersion(null));
  }, []);

  if (!currentBank) return null;

  const items: { id: NavItem; label: string; icon: typeof Home }[] = [
    { id: "home", label: t("dashboard"), icon: Home },
    { id: "data", label: t("memory"), icon: Database },
    { id: "knowledge", label: t("knowledgeBase"), icon: Network },
    { id: "recall", label: t("recall"), icon: Search },
    { id: "reflect", label: t("reflect"), icon: Sparkles },
    { id: "documents", label: t("documents"), icon: FileText },
    { id: "entities", label: t("entities"), icon: Users },
    { id: "profile", label: t("settings"), icon: Settings },
  ];

  const toggle = () => setCollapsed((value) => !value);

  return (
    <aside
      onClick={toggle}
      role="button"
      tabIndex={0}
      onKeyDown={(event) => {
        if (event.target !== event.currentTarget) return;
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          toggle();
        }
      }}
      aria-label={collapsed ? tSidebar("expand") : tSidebar("collapse")}
      aria-expanded={!collapsed}
      className={cn(
        "flex h-full cursor-pointer select-none flex-col border-r border-border bg-card transition-all duration-300",
        collapsed ? "w-16" : "w-64",
      )}
    >
      <nav className="flex-1 p-3 pt-4">
        <ul className="space-y-1">
          {items.map((item) => {
            const Icon = item.icon;
            const active = currentTab === item.id;
            return (
              <li key={item.id}>
                <Link
                  href={bankRoute(currentBank, `?view=${item.id}`)}
                  onClick={(event) => {
                    // A plain left click hands navigation to the parent so the
                    // view swaps without a document reload; modified clicks keep
                    // the browser's own behaviour so a new tab still works.
                    event.stopPropagation();
                    if (event.button === 0 && !event.ctrlKey && !event.metaKey) {
                      event.preventDefault();
                      onTabChange(item.id);
                    }
                  }}
                  className={cn(
                    "flex w-full cursor-pointer items-center gap-3 rounded-lg px-4 py-3 text-sm font-medium transition-all",
                    active
                      ? "bg-primary-gradient text-white shadow-sm"
                      : "text-muted-foreground hover:bg-accent hover:text-accent-foreground",
                    collapsed && "justify-center px-0",
                  )}
                  title={collapsed ? item.label : undefined}
                >
                  <Icon className="h-5 w-5 flex-shrink-0" />
                  {!collapsed && <span>{item.label}</span>}
                </Link>
              </li>
            );
          })}
        </ul>
      </nav>

      <div className="border-t border-border p-3">
        {apiVersion && (
          <div
            className={cn(
              "mb-2 select-none text-center text-xs text-muted-foreground/60",
              collapsed ? "px-0" : "px-1",
            )}
            title={`API v${apiVersion}`}
          >
            {collapsed ? `v${apiVersion}` : `v${apiVersion}`}
          </div>
        )}
        <button
          type="button"
          onClick={(event) => {
            // The rail's own handler would toggle a second time.
            event.stopPropagation();
            toggle();
          }}
          className={cn(
            "flex w-full cursor-pointer items-center gap-3 rounded-lg px-4 py-2 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground",
            collapsed && "justify-center px-0",
          )}
          title={collapsed ? tSidebar("expand") : tSidebar("collapse")}
        >
          {collapsed ? (
            <ChevronRight className="h-5 w-5" />
          ) : (
            <>
              <ChevronLeft className="h-5 w-5" />
              <span>{tSidebar("collapseAction")}</span>
            </>
          )}
        </button>
      </div>
    </aside>
  );
}