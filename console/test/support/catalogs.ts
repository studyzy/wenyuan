import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";

/** Shared catalog-loading helpers for the i18n suites. */
const MESSAGES_DIR = join(process.cwd(), "messages");

export function catalogFileNames(): string[] {
  return readdirSync(MESSAGES_DIR).filter((name) => name.endsWith(".json")).sort();
}

export function loadCatalog(locale: string): Record<string, unknown> {
  const raw = readFileSync(join(MESSAGES_DIR, `${locale}.json`), "utf8");
  return JSON.parse(raw) as Record<string, unknown>;
}

export function loadAllCatalogs(): Record<string, Record<string, unknown>> {
  const result: Record<string, Record<string, unknown>> = {};
  for (const fileName of catalogFileNames()) {
    const locale = fileName.replace(/\.json$/, "");
    result[locale] = loadCatalog(locale);
  }
  return result;
}