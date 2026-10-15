import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import {
  BANK_DETAIL_VIEWS,
  VIEWS,
  bankDetailPath,
  nextViewSearch,
  parseViewState,
} from "../lib/bank-detail";
import { sanitizeReturnTo } from "../lib/base-path";
import { isSupportedLocale, SUPPORTED_LOCALES } from "../i18n";

describe("root redirect", () => {
  it("redirects the locale root page to the dashboard", () => {
    const source = readFileSync(join(process.cwd(), "app/[locale]/page.tsx"), "utf8");
    expect(source).toContain('redirect("/dashboard")');
    expect(source).toContain("force-dynamic");
  });
});

describe("login returnTo sanitizer", () => {
  it("sanitizes unsafe returnTo values to the dashboard", () => {
    expect(sanitizeReturnTo("https://evil.example")).toBe("/dashboard");
    expect(sanitizeReturnTo("//evil.example")).toBe("/dashboard");
    expect(sanitizeReturnTo("javascript:alert(1)")).toBe("/dashboard");
    expect(sanitizeReturnTo("/banks/acme")).toBe("/banks/acme");
    expect(sanitizeReturnTo(null)).toBe("/dashboard");
  });
});

describe("bank detail query navigation", () => {
  it("parses view and sub-tabs from their contract parameters", () => {
    const state = parseViewState(
      new URLSearchParams("view=data&subTab=experience&knowledgeTab=models&bankConfigTab=webhooks"),
    );
    expect(state.view).toBe("data");
    expect(state.dataSubTab).toBe("experience");
    expect(state.knowledgeSubTab).toBe("models");
    expect(state.configurationSubTab).toBe("webhooks");
  });

  it("falls back to the default view for an absent or unknown value", () => {
    expect(parseViewState(new URLSearchParams()).view).toBe("home");
    expect(parseViewState(new URLSearchParams("view=nonsense")).view).toBe("home");
    expect(parseViewState(new URLSearchParams("view=data")).dataSubTab).toBe("world");
  });

  it("writes the sub-tab under its own contract parameter", () => {
    const search = nextViewSearch(new URLSearchParams("view=data"), { data: "observations" });
    expect(search).toContain("subTab=observations");
    expect(search).toContain("view=data");
    expect(bankDetailPath("team:alpha")).toBe("/banks/team%3Aalpha");
  });

  it("percent-encodes the bank path segment", () => {
    expect(bankDetailPath("a/b")).toBe("/banks/a%2Fb");
    expect(bankDetailPath("a b")).toBe("/banks/a%20b");
  });

  it("declares the view and sub-tab sets", () => {
    expect([...VIEWS]).toContain("data");
    expect(BANK_DETAIL_VIEWS).toBe(VIEWS);
  });
});

describe("locale static parameters", () => {
  it("treats each declared locale as valid and unknown values as invalid", () => {
    for (const locale of SUPPORTED_LOCALES) expect(isSupportedLocale(locale)).toBe(true);
    expect(isSupportedLocale("xx-YY")).toBe(false);
  });
});

describe("bank detail query contract on the locale page", () => {
  it("reads the four contract parameters the view state is built from", () => {
    const source = readFileSync(
      join(process.cwd(), "app/[locale]/banks/[bankId]/page.tsx"),
      "utf8",
    );
    for (const param of ["view", "subTab", "knowledgeTab", "bankConfigTab"]) {
      expect(source, `parameter ${param} missing`).toContain(`searchParams.get("${param}")`);
    }
  });

  it("keeps the view vocabulary and its parameters in the shared module", () => {
    const source = readFileSync(join(process.cwd(), "lib/bank-detail.ts"), "utf8");
    for (const param of ["view", "subTab", "knowledgeTab", "bankConfigTab"]) {
      expect(source, `parameter ${param} missing`).toContain(`"${param}"`);
    }
  });
});