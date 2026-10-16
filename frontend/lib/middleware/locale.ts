import { negotiateLocale } from "../../i18n/negotiate";
import { isSupportedLocale, type Locale } from "../../i18n/locales";

/**
 * Locale policy for middleware.
 *
 * The locale is NEVER a URL path segment: the prefix policy is `never`. The
 * resolved locale comes from the cookie pair, then Accept-Language, then the
 * default locale.
 */
export const LOCALE_PREFIX_POLICY = "never" as const;

export type LocaleResolution = {
  locale: Locale;
  prefixPolicy: typeof LOCALE_PREFIX_POLICY;
};

export function resolveLocale(input: {
  cookieHeader?: string;
  acceptLanguage?: string;
}): LocaleResolution {
  return {
    locale: negotiateLocale(input),
    prefixPolicy: LOCALE_PREFIX_POLICY,
  };
}

/**
 * Apply the locale policy to a pathname. Under the never-prefix policy the
 * locale never appears in the address bar, so the URL is left alone — but the
 * app tree is mounted under `app/[locale]/`, so the request has to reach that
 * segment by *internal rewrite*. The returned pathname is what the router is
 * rewritten to; the caller keeps the original for the user's URL.
 *
 * A path that already carries a locale segment is returned unchanged: it is
 * either this function's own earlier output (a rewrite is re-entered by the
 * middleware rather than short-circuited) or a genuine `/[locale]/...` request,
 * and prefixing it again would produce `/en/en/...`.
 */
export function rewriteForLocale(
  pathname: string,
  locale: Locale,
): { pathname: string; addedLocaleSegment: boolean } {
  if (hasLocaleSegment(pathname)) {
    return { pathname, addedLocaleSegment: false };
  }
  return { pathname: `/${locale}${pathname}`, addedLocaleSegment: true };
}

/** Whether the path's first segment is a supported locale. */
export function hasLocaleSegment(pathname: string): boolean {
  const [, first] = pathname.split("/");
  return first !== undefined && first !== "" && isSupportedLocale(first);
}