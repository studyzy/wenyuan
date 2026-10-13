import { cookies } from "next/headers";
import { negotiateLocale } from "../../i18n/negotiate";
import { getCatalog, lookupMessage } from "../../i18n/catalog";
import { DashboardView } from "./dashboard-view";

/**
 * Dashboard page. Hosts the all-banks overview; there is no separate
 * bank-listing page route.
 */
export default async function DashboardPage() {
  const cookieHeader = (await cookies())
    .getAll()
    .map((cookie: { name: string; value: string }) => `${cookie.name}=${cookie.value}`)
    .join("; ");
  const locale = negotiateLocale({ cookieHeader });
  const catalog = getCatalog(locale);

  return (
    <main className="console-surface">
      <h1>{lookupMessage(catalog, "dashboard.title")}</h1>
      <p>{lookupMessage(catalog, "dashboard.subtitle")}</p>
      <DashboardView banks={[]} />
    </main>
  );
}