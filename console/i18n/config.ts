/**
 * Locale registry in the shape views import.
 *
 * The canonical set and default live in `./locales`; this module re-exports
 * them under the names views use and adds the display names the picker renders.
 * Keeping one source for the set means a locale can never be supported for
 * negotiation but missing from the picker.
 */
import type { Locale } from "./locales";

export {
  DEFAULT_LOCALE as defaultLocale,
  SUPPORTED_LOCALES as locales,
  type Locale,
} from "./locales";

export const localeNames: Record<Locale, string> = {
  en: "English",
  es: "Español",
  fr: "Français",
  de: "Deutsch",
  pt: "Português",
  ja: "日本語",
  ko: "한국어",
  "zh-CN": "简体中文",
  "zh-TW": "正體中文（臺灣）",
  "yue-Hant": "粵語（繁體）",
};