"use client";

import Link from "next/link";
import { useTranslations } from "next-intl";
import { ConsoleNav } from "@/components/console-nav";
import { Button } from "@/components/ui/button";

/**
 * Shown when the console is deployed without a management-plane credential.
 * The tenant pages are an operator surface; rendering empty tables against an
 * unconfigured BFF would look like data loss, so the page says what is missing.
 */
export function AdminNotConfigured() {
  const t = useTranslations("tenants.notConfigured");
  return (
    <div className="min-h-screen bg-background">
      <ConsoleNav />
      <main className="mx-auto w-full max-w-2xl px-6 py-16">
        <h1 className="font-heading text-[24px] font-semibold">{t("title")}</h1>
        <p className="mt-2 text-sm text-muted-foreground">{t("body")}</p>
        <Button variant="outline" className="mt-6" asChild>
          <Link href="/dashboard">{t("back")}</Link>
        </Button>
      </main>
    </div>
  );
}
