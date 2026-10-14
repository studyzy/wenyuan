import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

/**
 * The profile panel's remaining views are client-side, so these assertions
 * cover what would otherwise only be visible in a browser: that no readable
 * surface is still a JSON dump, and which lifecycle actions exist.
 */
const PANEL_DIR = join(process.cwd(), "app/banks/[bankId]/panels");
const profile = readFileSync(join(PANEL_DIR, "bank-profile-panel.tsx"), "utf8");
const directives = readFileSync(join(PANEL_DIR, "directives-view.tsx"), "utf8");
const observations = readFileSync(join(PANEL_DIR, "observations-list.tsx"), "utf8");
const llm = readFileSync(join(PANEL_DIR, "llm-request-stats.tsx"), "utf8");

describe("profile panel has no remaining JSON dumps", () => {
  it("uses no JsonViewer anywhere in the panel", () => {
    // The panel is the operator surface for a bank: every section renders
    // through a view that knows its shape.
    expect(profile).not.toContain("JsonViewer");
    expect(profile).not.toContain("json-viewer");
  });

  it("binds each section to a dedicated view", () => {
    expect(profile).toContain("BankConfigEditor");
    expect(profile).toContain("DirectivesView");
    expect(profile).toContain("ObservationsList");
    expect(profile).toContain("LlmRequestStats");
  });

  it("points at the knowledge tab for mental models instead of duplicating the list", () => {
    expect(profile).toContain("modelsMovedHint");
  });

  it("stops loading data a section no longer reads", () => {
    // The models and directives loaders were removed with their dumps; leaving
    // them would fetch data nothing renders.
    expect(profile).not.toContain("mentalModelsApi.list");
    expect(profile).not.toContain("directivesApi.list");
  });
});

describe("directives view", () => {
  it("offers the full lifecycle: create, edit, toggle and delete", () => {
    expect(directives).toContain("directivesApi.create(bankId");
    expect(directives).toContain("directivesApi.update(bankId");
    expect(directives).toContain("directivesApi.remove(bankId");
    expect(directives).toContain("is_active: !row.is_active");
  });

  it("confirms deletion, because the directive stops applying", () => {
    expect(directives).toContain("AlertDialog");
    const en = JSON.parse(
      readFileSync(join(process.cwd(), "messages/en.json"), "utf8"),
    ) as { panels: { profile: Record<string, string> } };
    expect(en.panels.profile.deleteDirectiveDescription).toContain("cannot be undone");
  });

  it("refuses to submit without the name and content the route requires", () => {
    expect(directives).toContain(
      'disabled={saving || fields.name.trim() === "" || fields.content.trim() === ""}',
    );
  });

  it("reports a failed action rather than failing silently", () => {
    expect(directives).toContain('role="alert"');
    expect(directives).toContain("actionFailed");
  });
});

describe("observations list", () => {
  it("shows the proof count, which conveys evidence strength", () => {
    expect(observations).toContain("proof_count");
    expect(observations).toContain("columns.proofCount");
  });

  it("reads either envelope shape the route may return", () => {
    expect(observations).toContain("Array.isArray(payload)");
    expect(observations).toContain("items");
  });
});

describe("llm request stats", () => {
  it("is a histogram, one row per bucket", () => {
    expect(llm).toContain("data.buckets");
    expect(llm).toContain("<Table");
  });

  it("omits a status with no count rather than showing zero", () => {
    // A zero would read as "nothing failed" where the truth is "nothing was
    // recorded".
    expect(llm).toContain("([, count]) => count > 0");
  });

  it("marks a non-success status distinctly", () => {
    expect(llm).toContain('status.startsWith("2")');
    expect(llm).toContain("text-destructive");
  });

  it("keeps the token breakdown reachable without cluttering the cell", () => {
    expect(llm).toContain("tokensBreakdown");
    expect(llm).toContain("title={t(");
  });
});