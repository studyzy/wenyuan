"use client";

import { createContext, useCallback, useContext, type ReactNode } from "react";

import { DEFAULT_LOCALE, type Locale } from "./locales";
import { catalogFor, lookupMessage, type MessageCatalog } from "./catalog";

/**
 * Client-side message lookup.
 *
 * Views call a translator for the namespace they belong to and then ask it for
 * keys within that namespace, so a view never spells out a full key path and the
 * namespace is stated once. Missing keys fall back to the key itself, which
 * makes a gap visible in the UI instead of rendering nothing.
 */
type Translator = (key: string, values?: Record<string, string | number>) => string;

const LocaleContext = createContext<Locale>(DEFAULT_LOCALE);

export function LocaleProvider({
  locale,
  children,
}: {
  locale: Locale;
  children: ReactNode;
}) {
  return <LocaleContext.Provider value={locale}>{children}</LocaleContext.Provider>;
}

/** The locale resolved for this request. */
export function useLocale(): Locale {
  return useContext(LocaleContext);
}

/** Messages for the active locale, for code that needs the raw catalog. */
export function useCatalog(): MessageCatalog {
  return catalogFor(useContext(LocaleContext));
}

/**
 * Translate within a namespace, e.g. `useTranslations("bank.sidebar")("home")`.
 *
 * `{name}` placeholders in a message are replaced from `values`; a placeholder
 * with no value is left untouched so the gap is visible rather than silent.
 */
export function useTranslations(namespace?: string): Translator {
  const catalog = useCatalog();
  return useCallback(
    (key: string, values?: Record<string, string | number>) => {
      const full = namespace ? `${namespace}.${key}` : key;
      const message = lookupMessage(catalog, full);
      if (!values) return message;
      return message.replace(/\{(\w+)\}/g, (match, name: string) =>
        name in values ? String(values[name]) : match,
      );
    },
    [catalog, namespace],
  );
}

/** Translate outside a namespace, for code that has full key paths. */
export function useMessage(): Translator {
  return useTranslations();
}

/** Memoised translator for a namespace, for use inside a component body. */
export function useNamespacedMessages(namespace: string): Translator {
  return useTranslations(namespace);
}