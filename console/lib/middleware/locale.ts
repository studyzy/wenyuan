import { negotiateLocale } from "../../i18n/negotiate";
import type { Locale } from "../../i18n/locales";

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
 * locale is attached as a request header for downstream rendering while the URL
 * path is returned unchanged, so no locale segment ever appears.
 */
export function rewriteForLocale(pathname: string): { pathname: string; addedLocaleSegment: boolean } {
  return { pathname, addedLocaleSegment: false };
}