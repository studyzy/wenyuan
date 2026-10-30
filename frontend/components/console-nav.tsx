"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { adminApi } from "@/lib/admin-api";
import { Button } from "@/components/ui/button";

const LINKS = [
  { href: "/dashboard", key: "banks" },
  { href: "/tenants", key: "tenants" },
  { href: "/teams", key: "teams" },
  { href: "/overview", key: "overview" },
] as const;

export function ConsoleNav() {
  const pathname = usePathname();
  const router = useRouter();
  const t = useTranslations("tenants.nav");
  const [tenantId, setTenantId] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    adminApi
      .tenantSession()
      .then((session) => {
        if (live) setTenantId(session.tenant_id);
      })
      .catch(() => {
        if (live) setTenantId(null);
      });
    return () => {
      live = false;
    };
  }, []);

  async function exitTenant() {
    try {
      await adminApi.exitTenant();
    } finally {
      setTenantId(null);
      router.push("/dashboard");
      router.refresh();
    }
  }

  return (
    <nav className="flex items-center gap-1 border-b border-border bg-card/60 px-6 py-2 backdrop-blur">
      {LINKS.map((link) => {
        const active = pathname === link.href || pathname.startsWith(link.href + "/");
        return (
          <Link
            key={link.href}
            href={link.href}
            className={
              "rounded-[8px] px-3 py-1.5 text-[13px] font-medium transition-colors " +
              (active
                ? "bg-primary/[0.09] text-primary"
                : "text-muted-foreground hover:bg-accent hover:text-accent-foreground")
            }
          >
            {t(link.key)}
          </Link>
        );
      })}
      {tenantId && (
        <div className="ml-auto flex items-center gap-2 text-[12px] text-muted-foreground">
          <span className="font-mono">{t("activeTenant", { tenant: tenantId })}</span>
          <Button size="sm" variant="outline" onClick={() => void exitTenant()}>
            {t("exit")}
          </Button>
        </div>
      )}
    </nav>
  );
}
