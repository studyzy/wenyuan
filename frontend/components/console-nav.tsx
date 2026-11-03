"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { LogOut } from "lucide-react";
import { adminApi } from "@/lib/admin-api";
import { withBasePath } from "@/lib/base-path";
import { Button } from "@/components/ui/button";

/**
 * Where a session that is not the operator lands: the bank console. A tenant
 * key opens one tenant's schema and never the management plane — the gate
 * answers 403 on `/api/admin/**` to exactly that session.
 */
const BANKS_HOME = "/dashboard";

const LINKS = [
  { href: BANKS_HOME, key: "banks", operatorOnly: false },
  { href: "/tenants", key: "tenants", operatorOnly: true },
  { href: "/teams", key: "teams", operatorOnly: true },
  { href: "/overview", key: "overview", operatorOnly: true },
] as const;

/**
 * Which console links a session may see.
 *
 * The tenant, team and overview entries lead to the management plane, which a
 * tenant key is refused, so only the operator's session sees them. The subject
 * lives in the HttpOnly session cookie, so the server reports it rather than
 * the component decoding it.
 */
export function visibleLinks(operator: boolean) {
  return LINKS.filter((link) => operator || !link.operatorOnly);
}

export function ConsoleNav() {
  const pathname = usePathname();
  const router = useRouter();
  const t = useTranslations("tenants.nav");
  const tAuth = useTranslations("auth");
  const [operator, setOperator] = useState(false);
  const [tenantId, setTenantId] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    adminApi
      .tenantSession()
      .then((session) => {
        if (!live) return;
        setOperator(session.operator === true);
        setTenantId(session.tenant_id);
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

  async function signOut() {
    try {
      await fetch(withBasePath("/api/auth/logout"), { method: "POST" });
    } finally {
      window.location.href = withBasePath("/login");
    }
  }

  return (
    <nav className="flex items-center gap-1 border-b border-border bg-card/60 px-6 py-2 backdrop-blur">
      {visibleLinks(operator).map((link) => {
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
      <div className="ml-auto flex items-center gap-2 text-[12px] text-muted-foreground">
        {tenantId && (
          <>
            <span className="font-mono">{t("activeTenant", { tenant: tenantId })}</span>
            <Button size="sm" variant="outline" onClick={() => void exitTenant()}>
              {t("exit")}
            </Button>
          </>
        )}
        <Button size="sm" variant="ghost" onClick={() => void signOut()}>
          <LogOut className="mr-1.5 h-3.5 w-3.5" />
          {tAuth("signOut")}
        </Button>
      </div>
    </nav>
  );
}