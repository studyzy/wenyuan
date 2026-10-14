import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

/**
 * The recall and reflect result views are client-side, so these assertions
 * cover what would otherwise only be visible in a browser: that a readable view
 * exists, that the raw envelope stays reachable, and that absent values are
 * distinguished from zero.
 */
const PANEL_DIR = join(process.cwd(), "app/banks/[bankId]/panels");
const recallResults = readFileSync(join(PANEL_DIR, "recall-results.tsx"), "utf8");
const recallPanel = readFileSync(join(PANEL_DIR, "recall-panel.tsx"), "utf8");
const reflectResults = readFileSync(join(PANEL_DIR, "reflect-results.tsx"), "utf8");
const reflectPanel = readFileSync(join(PANEL_DIR, "reflect-panel.tsx"), "utf8");

const catalog = (locale: string) =>
  JSON.parse(readFileSync(join(process.cwd(), "messages", `${locale}.json`), "utf8")) as {
    panels: { recall: Record<string, string>; reflect: Record<string, string> };
  };

const LOCALES = ["en", "zh-CN", "zh-TW", "ja", "ko", "de", "fr", "es", "pt", "yue-Hant"];

describe("recall result view", () => {
  it("replaces the JSON dump as the primary view", () => {
    expect(recallPanel).toContain("RecallResults");
    expect(recallPanel).not.toContain("<JsonViewer value={result}");
  });

  it("keeps the raw envelope reachable, because the response is the contract", () => {
    expect(recallResults).toContain("tabRaw");
    expect(recallResults).toContain("<JsonViewer value={payload}");
  });

  it("ranks each result and shows its score breakdown", () => {
    expect(recallResults).toContain("rank={index + 1}");
    expect(recallResults).toContain("ScoreBreakdown");
    expect(recallResults).toContain("scores.reranker");
    expect(recallResults).toContain("scores.semantic");
  });

  it("omits a stage that did not surface the result rather than showing zero", () => {
    // Zero would read as "scored badly" where the truth is "never considered".
    expect(recallResults).toContain('typeof value === "number"');
  });

  it("reports a truncated list rather than silently shortening it", () => {
    expect(recallResults).toContain("data.truncated");
    expect(recallResults).toContain("truncated");
  });

  it("reads both tabs from the same fetched payload", () => {
    // A refetch on tab switch would double the cost of a recall.
    const fetchCalls = recallResults.match(/fetch\(|apiGet\(|apiPost\(/g) ?? [];
    expect(fetchCalls).toHaveLength(0);
  });

  it("ships every recall-view label in all ten locales", () => {
    for (const locale of LOCALES) {
      const keys = catalog(locale).panels.recall;
      for (const key of ["tabList", "tabRaw", "noResults", "truncated", "scoreFinal"]) {
        expect(keys[key], `${locale}.${key}`).toBeTruthy();
      }
    }
  });
});

describe("reflect result view", () => {
  it("replaces the JSON dump as the primary view", () => {
    expect(reflectPanel).toContain("ReflectResults");
    expect(reflectPanel).not.toContain("<JsonViewer value={result}");
  });

  it("shows the answer first and unsummarised", () => {
    expect(reflectResults).toContain("data.text");
    expect(reflectResults).toContain("whitespace-pre-wrap");
  });

  it("groups cited evidence by the kind of record it came from", () => {
    expect(reflectResults).toContain("based_on?.memories");
    expect(reflectResults).toContain("based_on?.mental_models");
    expect(reflectResults).toContain("based_on?.directives");
    expect(reflectResults).toContain("EvidenceGroup");
  });

  it("omits an evidence group that is empty rather than showing an empty heading", () => {
    expect(reflectResults).toContain("if (items.length === 0) return null;");
  });

  it("surfaces a structured-output error rather than swallowing it", () => {
    expect(reflectResults).toContain("structured_output_error");
    expect(reflectResults).toContain('role="alert"');
  });

  it("keeps the raw envelope reachable", () => {
    expect(reflectResults).toContain("tabRaw");
    expect(reflectResults).toContain("<JsonViewer value={payload}");
  });

  it("ships every reflect-view label in all ten locales", () => {
    for (const locale of LOCALES) {
      const keys = catalog(locale).panels.reflect;
      for (const key of [
        "tabAnswer",
        "tabEvidence",
        "tabRaw",
        "noAnswer",
        "noEvidence",
        "evidenceMemories",
        "evidenceModels",
        "evidenceDirectives",
        "structuredOutput",
        "usage",
      ]) {
        expect(keys[key], `${locale}.${key}`).toBeTruthy();
      }
    }
  });

  it("keeps the interpolated placeholders in every locale", () => {
    for (const locale of LOCALES) {
      const keys = catalog(locale).panels.reflect;
      expect(keys.tabEvidence, `${locale}.tabEvidence`).toContain("{total}");
      expect(keys.evidenceMemories, `${locale}.evidenceMemories`).toContain("{total}");
      expect(keys.usage, `${locale}.usage`).toContain("{input}");
      expect(keys.usage, `${locale}.usage`).toContain("{output}");
      expect(keys.usage, `${locale}.usage`).toContain("{total}");
    }
  });
});