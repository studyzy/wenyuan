import { catalogFor, lookupMessage, type MessageCatalog } from "../../i18n/catalog";

/**
 * Localized error envelope.
 *
 * The internal error key is removed from the outgoing payload, and one of the
 * message fields (error, message, detail, details) is replaced with the
 * translated string; when none is present a new error field is added.
 */
export const ERROR_KEY_FIELD = "__errorKey";
export const MESSAGE_FIELD_PRIORITY = ["error", "message", "detail", "details"] as const;

export type ErrorPayload = Record<string, unknown>;

export function localizeError(input: {
  payload?: ErrorPayload;
  errorKey: string;
  locale?: string;
}): ErrorPayload {
  const catalog: MessageCatalog = catalogFor(input.locale);
  const translated = lookupMessage(catalog, input.errorKey);

  const source: ErrorPayload = input.payload ? { ...input.payload } : {};
  delete source[ERROR_KEY_FIELD];

  const targetField = MESSAGE_FIELD_PRIORITY.find(
    (field) => typeof source[field] === "string",
  );

  if (targetField) {
    source[targetField] = translated;
  } else {
    source.error = translated;
  }

  return source;
}

/**
 * Build a localized error response body for a given error key, preserving any
 * extra context fields while stripping the internal key.
 */
export function buildErrorBody(errorKey: string, locale?: string, extra?: ErrorPayload): ErrorPayload {
  return localizeError({ payload: extra, errorKey, locale });
}