import { DEFAULT_LOCALE, isSupportedLocale, type Locale } from "./locales";

/**
 * Fixed alias table mapping common region/graph variants into the supported
 * locale set. Keys are compared case-insensitively after underscore
 * normalization.
 */
const REGION_ALIASES: Readonly<Record<string, Locale>> = {
  "zh-cn": "zh-CN",
  "zh-hans": "zh-CN",
  "zh-sg": "zh-CN",
  "zh-tw": "zh-TW",
  "zh-hant": "zh-TW",
  "zh-hk": "zh-TW",
  "zh-mo": "zh-TW",
  "yue": "yue-Hant",
  "yue-hk": "yue-Hant",
  "yue-hant": "yue-Hant",
  en_us: "en",
  "en-us": "en",
  "en-gb": "en",
};

/** Bare-language fallbacks used when no region alias matches. */
const LANGUAGE_FALLBACKS: Readonly<Record<string, Locale>> = {
  zh: "zh-CN",
  yue: "yue-Hant",
};

function canonicalize(tag: string): string {
  return tag.trim().replace(/_/g, "-");
}

/**
 * Normalize a single locale tag into a supported locale, or return undefined
 * when no mapping applies.
 *
 * Order: exact match, fixed alias table, case-insensitive match, language-only
 * fallback.
 */
export function normalizeLocaleTag(tag: string): Locale | undefined {
  const raw = canonicalize(tag);
  if (raw.length === 0) return undefined;

  if (isSupportedLocale(raw)) return raw;

  const lower = raw.toLowerCase();
  const alias = REGION_ALIASES[lower] ?? REGION_ALIASES[raw];
  if (alias) return alias;

  for (const locale of [
    "zh-CN",
    "zh-TW",
    "yue-Hant",
  ] as const) {
    if (locale.toLowerCase() === lower) return locale;
  }

  const language = lower.split("-")[0];
  return LANGUAGE_FALLBACKS[language];
}

/**
 * Normalize a raw locale value that may be malformed. Never throws; unparseable
 * values fall back to the raw string when it is supported, else undefined.
 */
export function safeNormalizeLocaleTag(value: string): Locale | undefined {
  let decoded = value;
  try {
    decoded = decodeURIComponent(value);
  } catch {
    decoded = value;
  }
  return normalizeLocaleTag(decoded);
}

export function resolveOrDefault(value: string | undefined | null): Locale {
  if (!value) return DEFAULT_LOCALE;
  return safeNormalizeLocaleTag(value) ?? DEFAULT_LOCALE;
}