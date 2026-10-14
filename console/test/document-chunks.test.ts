import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

/**
 * The document chunk viewer is client-side, so these assertions cover what
 * would otherwise only be visible in a browser: that chunks are paged, that a
 * chunk's position is shown, and that reprocess is available and honest about
 * what it did.
 */
const PANEL_DIR = join(process.cwd(), "app/banks/[bankId]/panels");
const chunks = readFileSync(join(PANEL_DIR, "document-chunks.tsx"), "utf8");
const panel = readFileSync(join(PANEL_DIR, "documents-panel.tsx"), "utf8");
const endpoints = readFileSync(join(process.cwd(), "lib/client/endpoints.ts"), "utf8");

const catalog = (locale: string) =>
  JSON.parse(readFileSync(join(process.cwd(), "messages", `${locale}.json`), "utf8")) as {
    panels: { documents: Record<string, string> };
  };

describe("document chunk viewer", () => {
  it("replaces the raw JSON dump in the dialog", () => {
    expect(panel).toContain("DocumentChunks");
    const dialogBlock = panel.slice(panel.indexOf("<Dialog"));
    expect(dialogBlock).not.toContain("JsonViewer");
  });

  it("pages the chunk listing rather than loading a whole document", () => {
    expect(chunks).toContain("CHUNK_PAGE_SIZE");
    expect(chunks).toContain("limit: CHUNK_PAGE_SIZE");
    expect(chunks).toContain("offset");
    expect(chunks).toContain("<Pagination");
  });

  it("sends the paging parameters through the client", () => {
    expect(endpoints).toContain("page?: { limit: number; offset: number }");
    expect(endpoints).toContain("limit: page ? String(page.limit) : undefined");
  });

  it("shows each chunk's position so it can be cited by index", () => {
    expect(chunks).toContain("row.chunk_index ?? offset + index");
  });

  it("offers reprocess and refreshes the listing after it", () => {
    expect(endpoints).toContain("reprocess:");
    expect(chunks).toContain("documentsApi.reprocess(bankId, documentId)");
    expect(chunks).toContain("chunks.reload()");
  });

  it("reports a reprocess failure rather than failing silently", () => {
    expect(chunks).toContain('role="alert"');
    expect(chunks).toContain("reprocessFailed");
  });

  it("uses a specific empty message instead of the generic one", () => {
    expect(chunks).toContain("emptyLabel={t(\"noChunks\")}");
  });

  it("ships every chunk-viewer label in all ten locales", () => {
    const required = [
      "chunkCount",
      "noChunks",
      "reprocess",
      "reprocessing",
      "reprocessed",
      "reprocessFailed",
    ];
    for (const locale of [
      "en",
      "zh-CN",
      "zh-TW",
      "ja",
      "ko",
      "de",
      "fr",
      "es",
      "pt",
      "yue-Hant",
    ]) {
      const keys = catalog(locale).panels.documents;
      for (const key of required) {
        expect(keys[key], `${locale}.${key}`).toBeTruthy();
      }
    }
  });

  it("keeps the count message interpolatable in every locale", () => {
    for (const locale of ["en", "zh-CN", "ja", "de"]) {
      expect(catalog(locale).panels.documents.chunkCount).toContain("{total}");
    }
  });
});