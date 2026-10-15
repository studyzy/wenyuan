"use client";

import {
  useLocale as useNextLocale,
  useMessages,
  useTranslations as useNextTranslations,
} from "next-intl";

import type { Locale } from "./locales";
import type { MessageCatalog } from "./catalog";

/**
 * Console translation shim.
 *
 * The console's own views import from here; components ported from the
 * reference import next-intl directly. Both must resolve against the same
 * provider and the same catalog, so these helpers delegate to next-intl rather
 * than keeping a second context: the root layout mounts the one provider, and
 * this module is a typed façade over it. Delegating also means ICU messages
 * (plurals, selects) are formatted correctly, which a hand-rolled lookup could
 * not do.
 */
type Translator = (key: string, values?: Record<string, string | number>) => string;

/** The locale resolved for this request. */
export function useLocale(): Locale {
  return useNextLocale() as Locale;
}

/** Messages for the active locale, for code that needs the raw catalog. */
export function useCatalog(): MessageCatalog {
  return useMessages() as MessageCatalog;
}

/** Translate within a namespace, e.g. `useTranslations("bank.sidebar")("home")`. */
export function useTranslations(namespace?: string): Translator {
  return useNextTranslations(namespace) as unknown as Translator;
}

/** Translate outside a namespace, for code that has full key paths. */
export function useMessage(): Translator {
  return useNextTranslations();
}

/** Memoised translator for a namespace, for use inside a component body. */
export function useNamespacedMessages(namespace: string): Translator {
  return useNextTranslations(namespace) as unknown as Translator;
}