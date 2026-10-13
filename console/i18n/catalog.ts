import { DEFAULT_LOCALE, isSupportedLocale, SUPPORTED_LOCALES, type Locale } from "./locales";
import en from "../messages/en.json";
import es from "../messages/es.json";
import fr from "../messages/fr.json";
import de from "../messages/de.json";
import pt from "../messages/pt.json";
import ja from "../messages/ja.json";
import ko from "../messages/ko.json";
import zhCN from "../messages/zh-CN.json";
import zhTW from "../messages/zh-TW.json";
import yueHant from "../messages/yue-Hant.json";

export type MessageCatalog = { [key: string]: string | MessageCatalog };

const CATALOGS: Readonly<Record<Locale, MessageCatalog>> = {
  en,
  es,
  fr,
  de,
  pt,
  ja,
  ko,
  "zh-CN": zhCN,
  "zh-TW": zhTW,
  "yue-Hant": yueHant,
};

export function getCatalog(locale: Locale): MessageCatalog {
  return CATALOGS[locale];
}

export function catalogFor(value: string | undefined | null): MessageCatalog {
  if (value && isSupportedLocale(value)) return CATALOGS[value];
  return CATALOGS[DEFAULT_LOCALE];
}

/**
 * Resolve a dotted message key against a catalog. Returns the key itself when
 * the path does not resolve to a non-empty string so callers never render an
 * empty label.
 */
export function lookupMessage(catalog: MessageCatalog, key: string): string {
  const parts = key.split(".");
  let cursor: string | MessageCatalog = catalog;
  for (const part of parts) {
    if (typeof cursor === "string") return key;
    const next: string | MessageCatalog | undefined = cursor[part];
    if (next === undefined) return key;
    cursor = next;
  }
  return typeof cursor === "string" && cursor.length > 0 ? cursor : key;
}

export function flatKeys(catalog: MessageCatalog, prefix = ""): string[] {
  const keys: string[] = [];
  for (const [key, value] of Object.entries(catalog)) {
    const path = prefix ? `${prefix}.${key}` : key;
    if (typeof value === "string") {
      keys.push(path);
    } else {
      keys.push(...flatKeys(value, path));
    }
  }
  return keys.sort();
}

export { SUPPORTED_LOCALES, DEFAULT_LOCALE };
export type { Locale };