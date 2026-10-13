/**
 * Locale registry.
 *
 * The supported locale set and the single default locale are fixed
 * compatibility surface. The locale is never emitted as a URL path segment.
 */
export const DEFAULT_LOCALE = "en" as const;

export const SUPPORTED_LOCALES = [
  "en",
  "es",
  "fr",
  "de",
  "pt",
  "ja",
  "ko",
  "zh-CN",
  "zh-TW",
  "yue-Hant",
] as const;

export type Locale = (typeof SUPPORTED_LOCALES)[number];

const SUPPORTED_SET: ReadonlySet<string> = new Set(SUPPORTED_LOCALES);

export function isSupportedLocale(value: string): value is Locale {
  return SUPPORTED_SET.has(value);
}