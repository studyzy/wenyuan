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
import { buildLoginViewModel } from "../lib/login-view";
import { isSupportedLocale, SUPPORTED_LOCALES } from "../i18n";

describe("root redirect", () => {
  it("redirects the root page to the dashboard with no locale segment", () => {
    const source = readFileSync(join(process.cwd(), "app/page.tsx"), "utf8");
    expect(source).toContain('redirect("/dashboard")');
    expect(source).toContain("force-dynamic");
  });
});

describe("login view model", () => {
  it("sanitizes unsafe returnTo values to the dashboard", () => {
    expect(buildLoginViewModel({ returnTo: "https://evil.example" }).returnTo).toBe("/dashboard");
    expect(buildLoginViewModel({ returnTo: "//evil.example" }).returnTo).toBe("/dashboard");
    expect(buildLoginViewModel({ returnTo: "/banks/acme" }).returnTo).toBe("/banks/acme");
  });

  it("exposes all declared locales for the language switcher", () => {
    expect(buildLoginViewModel({}).languages).toEqual(SUPPORTED_LOCALES);
  });
});

describe("bank detail query navigation", () => {
  it("parses view and sub-tabs from query parameters", () => {
    const state = parseViewState(new URLSearchParams("view=data&data=documents"));
    expect(state.view).toBe("data");
    expect(state.dataSubTab).toBe("documents");
  });

  it("defaults to the overview view", () => {
    expect(parseViewState(new URLSearchParams()).view).toBe("overview");
  });

  it("updates a query parameter without leaving the bank path", () => {
    const search = nextViewSearch(new URLSearchParams("view=data"), { data: "operations" });
    expect(search).toContain("data=operations");
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

describe("page route context", () => {
  it("awaits the async searchParams promised by the framework", () => {
    for (const file of ["app/login/page.tsx", "app/banks/[bankId]/page.tsx"]) {
      const source = readFileSync(join(process.cwd(), file), "utf8");
      expect(source, file).toContain("Promise<");
      expect(source, file).toMatch(/await searchParams/);
    }
  });

  it("awaits the async dynamic route params on the bank detail page", () => {
    const source = readFileSync(join(process.cwd(), "app/banks/[bankId]/page.tsx"), "utf8");
    expect(source).toMatch(/await params/);
  });
});