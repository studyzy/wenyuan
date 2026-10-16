"use client";

import { useEffect, useState } from "react";
import { useTranslations } from "next-intl";
import { useBank } from "@/lib/bank-context";
import { bankRoute } from "@/lib/bank-url";
import {
  Home,
  Search,
  Sparkles,
  Database,
  FileText,
  Users,
  Network,
  ChevronLeft,
  ChevronRight,
  Settings,
} from "lucide-react";
import { cn } from "@/lib/utils";
import Link from "next/link";
import { client } from "@/lib/api";

type NavItem =
  | "home"
  | "recall"
  | "reflect"
  | "data"
  | "documents"
  | "entities"
  | "knowledge"
  | "profile";

interface SidebarProps {
  currentTab: NavItem;
  onTabChange: (tab: NavItem) => void;
}

export function Sidebar({ currentTab, onTabChange }: SidebarProps) {
  const t = useTranslations("bank.sidebar");
  const tBank = useTranslations("bank");
  const { currentBank } = useBank();
  const [isCollapsed, setIsCollapsed] = useState(true);
  const [apiVersion, setApiVersion] = useState<string | null>(null);

  useEffect(() => {
    client
      .getVersion()
      .then((v) => setApiVersion(v.api_version))
      .catch(() => setApiVersion(null));
  }, []);

  if (!currentBank) {
    return null;
  }

  const navItems = [
    { id: "home" as NavItem, label: t("home"), icon: Home },
    { id: "data" as NavItem, label: t("memories"), icon: Database },
    { id: "knowledge" as NavItem, label: t("knowledge"), icon: Network },
    { id: "recall" as NavItem, label: t("recall"), icon: Search },
    { id: "reflect" as NavItem, label: t("reflect"), icon: Sparkles },
    { id: "documents" as NavItem, label: t("documents"), icon: FileText },
    { id: "entities" as NavItem, label: t("entities"), icon: Users },
    { id: "profile" as NavItem, label: tBank("bankConfiguration"), icon: Settings },
  ];

  // Toggle when the user clicks the aside chrome itself — nav links and the
  // collapse button stop propagation so clicking an item navigates without
  // also flipping the collapsed state.
  const toggleCollapsed = () => setIsCollapsed((v) => !v);

  return (
    <aside
      onClick={toggleCollapsed}
      role="button"
      tabIndex={0}
      onKeyDown={(e) => {
        // Only when the aside itself has focus. Without this guard, pressing
        // Enter on a focused nav link bubbles up here and collapses the rail
        // on every keyboard navigation.
        if (e.target !== e.currentTarget) return;
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          toggleCollapsed();
        }
      }}
      aria-label={isCollapsed ? t("expandSidebar") : t("collapseSidebar")}
      aria-expanded={!isCollapsed}
      className={cn(
        "bg-sidebar border-r border-sidebar-border flex flex-col h-full transition-[width] duration-300 ease-[var(--ease-out-quint)] cursor-pointer select-none",
        isCollapsed ? "w-16" : "w-64"
      )}
    >
      <nav className="flex-1 p-3 pt-4">
        <ul className="space-y-0.5">
          {navItems.map((item) => {
            const Icon = item.icon;
            const isActive = currentTab === item.id;
            const href = bankRoute(currentBank, `?view=${item.id}`);

            return (
              <li key={item.id}>
                <Link
                  href={href}
                  onClick={(e) => {
                    // Don't bubble — clicking an item navigates, it doesn't
                    // toggle the sidebar.
                    e.stopPropagation();
                    // For left-click, prevent default and use the callback
                    // This allows the parent to handle navigation without full page reload
                    if (e.button === 0 && !e.ctrlKey && !e.metaKey) {
                      e.preventDefault();
                      onTabChange(item.id);
                      // Give the header logo a playful spin on navigation.
                      window.dispatchEvent(new CustomEvent("wenyuan:logo-spin"));
                    }
                    // Middle-click or Ctrl/Cmd+click will naturally open in new tab
                  }}
                  className={cn(
                    "group relative w-full flex items-center gap-3 rounded-[10px] px-3.5 py-2.5 text-[13px] font-medium tracking-[-0.006em] transition-colors duration-200 cursor-pointer",
                    isActive
                      ? "bg-primary/[0.09] text-primary dark:bg-primary/[0.14]"
                      : "text-muted-foreground hover:bg-accent hover:text-accent-foreground",
                    isCollapsed && "justify-center px-0"
                  )}
                  title={isCollapsed ? item.label : undefined}
                >
                  {/* The active marker is a lit rail segment rather than a
                      filled pill: it reads as the current position on a
                      track, which is what the nav is. */}
                  {isActive && (
                    <span
                      aria-hidden
                      className={cn(
                        "absolute left-0 top-1/2 h-5 w-[2.5px] -translate-y-1/2 rounded-full bg-primary shadow-[0_0_10px_0_color-mix(in_oklab,var(--primary)_70%,transparent)]",
                        isCollapsed && "left-[3px]"
                      )}
                    />
                  )}
                  <Icon
                    className={cn(
                      "h-[18px] w-[18px] flex-shrink-0 transition-colors",
                      isActive ? "text-primary" : "text-muted-foreground/80 group-hover:text-foreground"
                    )}
                  />
                  {!isCollapsed && <span className="truncate">{item.label}</span>}
                </Link>
              </li>
            );
          })}
        </ul>
      </nav>

      {/* Collapse/Expand button at bottom — kept as an explicit affordance
          alongside the ambient click-to-toggle on the aside chrome. */}
      <div className="border-t border-border p-3">
        {apiVersion && (
          <div
            className={cn(
              "mb-2 flex items-center justify-center gap-1.5 text-center font-mono text-[10.5px] tracking-[0.02em] text-muted-foreground/70 select-none",
              isCollapsed ? "px-0" : "px-1"
            )}
            title={`WenYuan API v${apiVersion}`}
          >
            <span
              aria-hidden
              className="h-1.5 w-1.5 shrink-0 rounded-full bg-success shadow-[0_0_6px_0_color-mix(in_oklab,var(--hs-success)_80%,transparent)]"
            />
            {isCollapsed ? `v${apiVersion}` : `WenYuan v${apiVersion}`}
          </div>
        )}
        <button
          onClick={(e) => {
            // Don't double-toggle when the aside's onClick also fires.
            e.stopPropagation();
            toggleCollapsed();
          }}
          className={cn(
            "flex w-full items-center gap-3 rounded-[10px] px-3.5 py-2 text-[13px] text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground cursor-pointer",
            isCollapsed && "justify-center px-0"
          )}
          title={isCollapsed ? t("expandSidebar") : t("collapseSidebar")}
        >
          {isCollapsed ? (
            <ChevronRight className="h-[18px] w-[18px]" />
          ) : (
            <>
              <ChevronLeft className="h-[18px] w-[18px]" />
              <span>{t("collapse")}</span>
            </>
          )}
        </button>
      </div>
    </aside>
  );
}
