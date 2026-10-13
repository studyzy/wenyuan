import { cookies } from "next/headers";

import { negotiateLocale } from "../../i18n/negotiate";
import { getCatalog, lookupMessage } from "../../i18n/catalog";
import { AppShell } from "../../components/shell/app-shell";
import { BanksOverview } from "../../components/shell/banks-overview";

/**
 * Dashboard page.
 *
 * Hosts the all-banks overview: there is no separate bank-listing route, so the
 * dashboard is both the landing target and the bank directory.
 */
export default async function DashboardPage() {
  const cookieHeader = (await cookies())
    .getAll()
    .map((cookie: { name: string; value: string }) => `${cookie.name}=${cookie.value}`)
    .join("; ");
  const locale = negotiateLocale({ cookieHeader });
  const catalog = getCatalog(locale);

  return (
    <AppShell>
      <main className="mx-auto flex w-full max-w-[1100px] flex-1 flex-col gap-4 overflow-y-auto p-6">
        <div className="flex flex-col gap-1">
          <h1 className="text-[20px] font-semibold tracking-tight text-foreground">
            {lookupMessage(catalog, "nav.banks")}
          </h1>
          <p className="text-[13px] text-muted-foreground">
            {lookupMessage(catalog, "dashboard.subtitle")}
          </p>
        </div>
        <BanksOverview />
      </main>
    </AppShell>
  );
}