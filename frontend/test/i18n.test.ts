import { describe, expect, it } from "vitest";
import {
  DEFAULT_LOCALE,
  SUPPORTED_LOCALES,
  negotiateFromAcceptLanguage,
  negotiateLocale,
  normalizeLocaleTag,
} from "../i18n";
import { catalogFileNames, loadAllCatalogs } from "./support/catalogs";

function flatten(value: unknown, prefix = ""): string[] {
  if (typeof value === "string") return [prefix];
  if (value && typeof value === "object") {
    return Object.entries(value as Record<string, unknown>).flatMap(([key, child]) =>
      flatten(child, prefix ? `${prefix}.${key}` : key),
    );
  }
  return [];
}

describe("locale registry", () => {
  it("declares exactly the supported locale set with one default", () => {
    expect([...SUPPORTED_LOCALES]).toEqual([
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
    ]);
    expect(DEFAULT_LOCALE).toBe("en");
  });
});

describe("catalog parity", () => {
  it("has exactly one catalog file per declared locale", () => {
    expect(catalogFileNames().sort()).toEqual([...SUPPORTED_LOCALES].sort().map((l) => `${l}.json`));
  });

  it("gives every non-default catalog exactly the default key set", () => {
    const catalogs = loadAllCatalogs();
    const defaultKeys = flatten(catalogs[DEFAULT_LOCALE]).sort();
    expect(defaultKeys.length).toBeGreaterThan(0);
    for (const locale of SUPPORTED_LOCALES) {
      const keys = flatten(catalogs[locale]).sort();
      expect(keys, `key set for ${locale}`).toEqual(defaultKeys);
    }
  });

  it("gives every leaf a non-empty string", () => {
    const catalogs = loadAllCatalogs();
    for (const locale of SUPPORTED_LOCALES) {
      for (const key of flatten(catalogs[locale])) {
        const value = key
          .split(".")
          .reduce<unknown>((acc, part) => (acc as Record<string, unknown>)[part], catalogs[locale]);
        expect(typeof value, `${locale}:${key}`).toBe("string");
        expect((value as string).length, `${locale}:${key}`).toBeGreaterThan(0);
      }
    }
  });
});

describe("locale alias normalization", () => {
  it("maps region variants to supported locales", () => {
    expect(normalizeLocaleTag("zh-Hans")).toBe("zh-CN");
    expect(normalizeLocaleTag("zh-Hant")).toBe("zh-TW");
    expect(normalizeLocaleTag("en-US")).toBe("en");
  });

  it("maps bare yue to yue-Hant and bare zh to zh-CN", () => {
    expect(normalizeLocaleTag("yue")).toBe("yue-Hant");
    expect(normalizeLocaleTag("zh")).toBe("zh-CN");
  });

  it("matches supported locales case-insensitively", () => {
    expect(normalizeLocaleTag("ZH-TW")).toBe("zh-TW");
    expect(normalizeLocaleTag("YUE-HANT")).toBe("yue-Hant");
  });
});

describe("locale negotiation", () => {
  it("prefers the locale cookie", () => {
    expect(negotiateLocale({ cookieHeader: "NEXT_LOCALE=fr", acceptLanguage: "de" })).toBe("fr");
  });

  it("accepts the alternate locale cookie name", () => {
    expect(negotiateLocale({ cookieHeader: "wenyuan_cp_locale=ja" })).toBe("ja");
  });

  it("orders Accept-Language by quality", () => {
    expect(negotiateFromAcceptLanguage("en;q=0.4, de;q=0.9")).toBe("de");
  });

  it("falls back to the default locale when nothing resolves", () => {
    expect(negotiateLocale({})).toBe(DEFAULT_LOCALE);
  });

  it("tolerates malformed cookie values without failing", () => {
    expect(negotiateLocale({ cookieHeader: "NEXT_LOCALE=%E0%A4%A" })).toBe(DEFAULT_LOCALE);
    expect(negotiateLocale({ cookieHeader: "NEXT_LOCALE=%zz" })).toBe(DEFAULT_LOCALE);
  });
});