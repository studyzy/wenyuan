import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

/**
 * The bank configuration editor is client-side, so these assertions cover what
 * would otherwise only be visible in a browser: what the form sends, and how it
 * distinguishes an override from an inherited value.
 */
const PANEL_DIR = join(process.cwd(), "app/banks/[bankId]/panels");
const editor = readFileSync(join(PANEL_DIR, "bank-config-editor.tsx"), "utf8");
const profile = readFileSync(join(PANEL_DIR, "bank-profile-panel.tsx"), "utf8");
const endpoints = readFileSync(join(process.cwd(), "lib/client/endpoints.ts"), "utf8");

const catalog = (locale: string) =>
  JSON.parse(readFileSync(join(process.cwd(), "messages", `${locale}.json`), "utf8")) as {
    panels: { profile: Record<string, string> };
  };

describe("bank configuration editor", () => {
  it("is rendered by the configuration sub-tab instead of a raw dump", () => {
    expect(profile).toContain("BankConfigEditor");
    // The JSON dump must not remain as the configuration body.
    const configBlock = profile.slice(profile.indexOf('subTab === "configuration"'));
    const body = configBlock.slice(0, configBlock.indexOf('subTab === "webhooks"'));
    expect(body).not.toContain("<JsonViewer value={config.data}");
  });

  it("grades each trait on the bounded scale the route accepts", () => {
    // The trait keys and bounds come from the shared contract, not from the
    // component's own copy, so an out-of-range value cannot be offered.
    expect(endpoints).toContain("DISPOSITION_MIN = 1");
    expect(endpoints).toContain("DISPOSITION_MAX = 5");
    expect(endpoints).toContain("DISPOSITION_NEUTRAL = 3");
    expect(editor).toContain("DISPOSITION_MIN");
    expect(editor).toContain("DISPOSITION_MAX");
  });

  it("sends the updates object the route reads", () => {
    expect(endpoints).toContain("updateConfig:");
    expect(endpoints).toContain("apiPatch<BankConfig>");
    expect(editor).toContain("banksApi.updateConfig(bankId, updates)");
  });

  it("sends only the changed fields, so an untouched trait keeps its inheritance", () => {
    expect(editor).toContain("updates[key] = traits[key]");
    expect(editor).toContain("initial.config.reflect_mission");
  });

  it("marks whether each field is an override or the inherited default", () => {
    // Once resolved the two look identical, so the distinction is read from the
    // overrides object by presence rather than by comparing values.
    expect(editor).toContain("hasOwnProperty.call(overrides, key)");
    expect(editor).toContain("originOverride");
    expect(editor).toContain("originInherited");
  });

  it("confirms before resetting, because a reset cannot be undone", () => {
    expect(editor).toContain("AlertDialog");
    expect(editor).toContain("banksApi.resetConfig(bankId)");
    const en = catalog("en");
    expect(en.panels.profile.resetConfirmDescription).toContain("cannot be undone");
  });

  it("disables saving when nothing changed", () => {
    expect(editor).toContain("disabled={saving || resetting || !dirty}");
  });

  it("ships every editor label in all ten locales", () => {
    const required = [
      "dispositionsTitle",
      "dispositionsDescription",
      "traitSkepticism",
      "traitLiteralism",
      "traitEmpathy",
      "missionTitle",
      "missionDescription",
      "missionPlaceholder",
      "originOverride",
      "originInherited",
      "save",
      "saving",
      "reset",
      "resetting",
      "unsaved",
      "saveFailed",
      "resetFailed",
      "cancel",
      "resetConfirmTitle",
      "resetConfirmDescription",
      "resetConfirmAction",
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
      const profileKeys = catalog(locale).panels.profile;
      for (const key of required) {
        expect(profileKeys[key], `${locale}.${key}`).toBeTruthy();
      }
    }
  });

  it("translates rather than reusing the English copy", () => {
    // A locale that simply mirrored English would pass a presence check, so the
    // two most different scripts are compared against it.
    const en = catalog("en").panels.profile;
    for (const locale of ["zh-CN", "ja"]) {
      const localized = catalog(locale).panels.profile;
      expect(localized.dispositionsTitle).not.toBe(en.dispositionsTitle);
      expect(localized.save).not.toBe(en.save);
    }
  });
});