import { DEFAULT_LOCALE, type Locale } from "./locales";
import { normalizeLocaleTag, safeNormalizeLocaleTag } from "./normalize";

/** Cookie name carrying the locale for the framework layer. */
export const LOCALE_COOKIE = "NEXT_LOCALE";
/** Alternate locale cookie name accepted during negotiation. */
export const LOCALE_COOKIE_ALT = "wenyuan_cp_locale";

type CookieSource = string | undefined | null;

/**
 * Extract a named cookie value from a raw Cookie header. Returns undefined when
 * absent. Malformed percent sequences are tolerated: the raw value is returned.
 */
export function readCookie(cookieHeader: CookieSource, name: string): string | undefined {
  if (!cookieHeader) return undefined;
  for (const part of cookieHeader.split(";")) {
    const eq = part.indexOf("=");
    if (eq === -1) continue;
    const key = part.slice(0, eq).trim();
    if (key !== name) continue;
    return part.slice(eq + 1).trim();
  }
  return undefined;
}

/**
 * Resolve the locale from the cookie pair, then an Accept-Language list by
 * quality, then the default locale.
 */
export function negotiateLocale(input: {
  cookieHeader?: string;
  acceptLanguage?: string;
}): Locale {
  const primary = readCookie(input.cookieHeader, LOCALE_COOKIE);
  const alternate = readCookie(input.cookieHeader, LOCALE_COOKIE_ALT);
  for (const cookieValue of [primary, alternate]) {
    if (cookieValue) {
      const resolved = safeNormalizeLocaleTag(cookieValue);
      if (resolved) return resolved;
    }
  }

  const fromHeader = negotiateFromAcceptLanguage(input.acceptLanguage);
  return fromHeader ?? DEFAULT_LOCALE;
}

/**
 * Parse an Accept-Language header and return the best supported locale, ordered
 * by descending quality. Unparseable quality values are treated as 0.
 */
export function negotiateFromAcceptLanguage(header: CookieSource): Locale | undefined {
  if (!header) return undefined;
  const candidates = header
    .split(",")
    .map((entry, index) => {
      const [tagPart, ...params] = entry.split(";");
      const tag = tagPart.trim();
      let quality = 1;
      for (const param of params) {
        const [key, value] = param.split("=").map((s) => s.trim());
        if (key === "q") {
          const parsed = Number.parseFloat(value);
          quality = Number.isFinite(parsed) ? parsed : 0;
        }
      }
      return { tag, quality, index };
    })
    .filter((c) => c.tag.length > 0 && c.quality > 0)
    .sort((a, b) => (b.quality - a.quality) || (a.index - b.index));

  for (const candidate of candidates) {
    const resolved = normalizeLocaleTag(candidate.tag);
    if (resolved) return resolved;
  }
  return undefined;
}