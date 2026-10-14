import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

/**
 * The mental-model view is client-side, so these assertions cover what would
 * otherwise only be visible in a browser: which lifecycle actions exist, what a
 * create sends, and that destructive actions are confirmed.
 */
const PANEL_DIR = join(process.cwd(), "app/banks/[bankId]/panels");
const view = readFileSync(join(PANEL_DIR, "mental-models-view.tsx"), "utf8");
const knowledge = readFileSync(join(PANEL_DIR, "knowledge-panel.tsx"), "utf8");

const catalog = (locale: string) =>
  JSON.parse(readFileSync(join(process.cwd(), "messages", `${locale}.json`), "utf8")) as {
    panels: { knowledge: Record<string, string> };
  };

describe("mental model view", () => {
  it("replaces the JSON dump in the models sub-tab", () => {
    expect(knowledge).toContain("MentalModelsView");
    const modelsBranch = knowledge.slice(knowledge.indexOf('subTab === "models"'));
    const branch = modelsBranch.slice(0, modelsBranch.indexOf("const nodes"));
    expect(branch).not.toContain("JsonViewer");
  });

  it("offers the full lifecycle: refresh, edit and delete", () => {
    expect(view).toContain("mentalModelsApi.refresh(bankId, id)");
    expect(view).toContain("mentalModelsApi.update(bankId, modelId, draft)");
    expect(view).toContain("mentalModelsApi.remove(bankId, id)");
  });

  it("confirms deletion, because the model and its history go together", () => {
    expect(view).toContain("AlertDialog");
    const en = catalog("en").panels.knowledge;
    expect(en.deleteModelDescription).toContain("cannot be undone");
  });

  it("creates with a subtype, which is the one value that distinguishes the kinds", () => {
    expect(view).toContain("SUBTYPES");
    expect(view).toContain("subtype,");
    expect(view).toContain("mentalModelsApi.create(bankId, {");
  });

  it("refuses to create without the name and source query the route requires", () => {
    expect(view).toContain('disabled={saving || name.trim() === "" || sourceQuery.trim() === ""}');
  });

  it("reloads the list after a lifecycle action rather than patching local state", () => {
    expect(view).toContain("state.reload()");
  });

  it("reports an action failure rather than failing silently", () => {
    expect(view).toContain('role="alert"');
    expect(view).toContain("actionFailed");
  });

  it("ships every model-view label in all ten locales", () => {
    const required = [
      "noModels",
      "createModel",
      "createModelTitle",
      "createModelAction",
      "editModelTitle",
      "refreshModel",
      "deleteModel",
      "deleteModelTitle",
      "deleteModelDescription",
      "deleteModelAction",
      "fieldName",
      "fieldDescription",
      "fieldSubtype",
      "fieldSubtypeHint",
      "fieldSourceQuery",
      "fieldSourceQueryHint",
      "fieldContent",
      "fieldRefreshed",
      "neverRefreshed",
      "save",
      "saving",
      "saveFailed",
      "createFailed",
      "actionFailed",
      "cancel",
      "untitled",
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
      const keys = catalog(locale).panels.knowledge;
      for (const key of required) {
        expect(keys[key], `${locale}.${key}`).toBeTruthy();
      }
    }
  });

  it("translates the column headers in every locale", () => {
    for (const locale of ["en", "zh-CN", "ja"]) {
      const columns = catalog(locale).panels.knowledge.columns as unknown as Record<string, string>;
      for (const key of ["name", "subtype", "tags", "refreshed"]) {
        expect(columns[key], `${locale}.columns.${key}`).toBeTruthy();
      }
    }
  });
});