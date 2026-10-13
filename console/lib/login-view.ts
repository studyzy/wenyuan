import { sanitizeReturnTo } from "./server/paths";
import { SUPPORTED_LOCALES, DEFAULT_LOCALE, type Locale } from "../i18n/locales";

/**
 * Login page view model.
 *
 * The returnTo value is sanitized so post-login navigation falls back to the
 * dashboard for unsafe values and uses the app-relative path for safe values.
 */
export type LoginViewModel = {
  returnTo: string;
  locale: Locale;
  languages: readonly Locale[];
};

export function buildLoginViewModel(input: {
  returnTo?: string | null;
  locale?: string | null;
}): LoginViewModel {
  return {
    returnTo: sanitizeReturnTo(input.returnTo),
    locale: (input.locale as Locale) ?? DEFAULT_LOCALE,
    languages: SUPPORTED_LOCALES,
  };
}