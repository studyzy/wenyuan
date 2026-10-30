import { describe, expect, it } from "vitest";

import { SUPPORTED_LOCALES } from "@/i18n/locales";
import { buildManifest } from "@/lib/manifest";

/**
 * The PWA manifest is fetched by the browser at install/pin time and carries
 * the name the OS shows. A Chinese session must get the Chinese name, not the
 * pinyin wordmark, while every non-brand field stays identical across locales.
 */
describe("locale-aware web app manifest", () => {
  it.each([
    ["zh-CN", "文渊控制台", "文渊"],
    ["zh-TW", "文淵控制台", "文淵"],
    ["yue-Hant", "文淵控制台", "文淵"],
    ["en", "WenYuan Control Plane", "WenYuan"],
    ["ja", "WenYuan コントロールプレーン", "WenYuan"],
  ])("%s names the app in its own language", (locale, name, shortName) => {
    const manifest = buildManifest(locale as (typeof SUPPORTED_LOCALES)[number]);
    expect(manifest.name).toBe(name);
    expect(manifest.short_name).toBe(shortName);
  });

  it("never falls back to a message key", () => {
    for (const locale of SUPPORTED_LOCALES) {
      const manifest = buildManifest(locale);
      expect(manifest.name).not.toContain(".");
      expect(manifest.short_name).not.toContain(".");
    }
  });

  it("keeps the locale-independent fields identical everywhere", () => {
    const baseline = buildManifest("en");
    for (const locale of SUPPORTED_LOCALES) {
      const manifest = buildManifest(locale);
      expect(manifest).toMatchObject({
        description: baseline.description,
        start_url: "/dashboard",
        display: "standalone",
        background_color: "#F4F6FB",
        theme_color: "#0052D9",
        icons: baseline.icons,
      });
    }
  });
});
