import { cookies } from "next/headers";
import { notFound } from "next/navigation";
import { parseViewState } from "../../../lib/bank-detail";
import { negotiateLocale } from "../../../i18n/negotiate";
import { resolveOrDefault } from "../../../i18n/normalize";
import { isSupportedLocale } from "../../../i18n/locales";
import { getCatalog, lookupMessage } from "../../../i18n/catalog";
import { BankDetailView } from "./bank-detail-view";

/**
 * Flatten a resolved query-parameter record into URLSearchParams, keeping the
 * first value of any repeated parameter so view-state parsing stays total.
 */
function toSearchParams(
  record: Record<string, string | string[] | undefined>,
): URLSearchParams {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(record)) {
    if (Array.isArray(value)) {
      if (value.length > 0) params.set(key, value[0]);
    } else if (value !== undefined) {
      params.set(key, value);
    }
  }
  return params;
}

/**
 * Bank detail page addressed by a percent-encoded bank id.
 *
 * Selects its visible view and sub-tabs from query parameters. Unknown locale
 * values are treated as not-found; each declared locale is a valid value.
 */
export default async function BankDetailPage({
  params,
  searchParams,
}: {
  params: Promise<{ bankId: string }>;
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const { bankId: rawBankId } = await params;
  const bankId = decodeURIComponent(rawBankId);
  const resolvedSearchParams = await searchParams;
  const localeParam = resolvedSearchParams.locale;
  const requestedLocale = Array.isArray(localeParam) ? localeParam[0] : localeParam;

  const cookieHeader = (await cookies())
    .getAll()
    .map((cookie: { name: string; value: string }) => `${cookie.name}=${cookie.value}`)
    .join("; ");
  const locale = resolveOrDefault(requestedLocale ?? negotiateLocale({ cookieHeader }));

  if (requestedLocale && !isSupportedLocale(requestedLocale)) {
    notFound();
  }

  const state = parseViewState(toSearchParams(resolvedSearchParams));
  const catalog = getCatalog(locale);

  return (
    <main className="console-surface">
      <h1>{lookupMessage(catalog, "bank.detailTitle")}</h1>
      <BankDetailView bankId={bankId} initialState={state} />
    </main>
  );
}

export function generateStaticParams() {
  return [];
}