import { cookies, headers } from "next/headers";
import { getRequestConfig } from "next-intl/server";

import { getCatalog } from "./catalog";
import { negotiateLocale } from "./negotiate";

/**
 * next-intl request configuration.
 *
 * The locale is resolved through the console's own negotiation (cookie pair,
 * then Accept-Language, then the default) — the same path the middleware and
 * the root layout use — so next-intl never forms a second opinion about which
 * locale is current. Messages come from the one catalog set under
 * `console/messages`; the console's own i18n shim reads the same provider.
 */
export default getRequestConfig(async () => {
  const cookieHeader = (await cookies())
    .getAll()
    .map((cookie: { name: string; value: string }) => `${cookie.name}=${cookie.value}`)
    .join("; ");
  const acceptLanguage = (await headers()).get("accept-language") ?? undefined;
  const locale = negotiateLocale({ cookieHeader, acceptLanguage });

  return {
    locale,
    messages: getCatalog(locale),
    // A fixed time zone keeps server and client formatting in agreement; the
    // console renders timestamps itself and only needs a stable default here.
    timeZone: "UTC",
  };
});