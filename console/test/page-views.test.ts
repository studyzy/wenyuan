import { describe, expect, it } from "vitest";
import { existsSync, readFileSync } from "node:fs";
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
describe("bank detail panels", () => {
  const panelsDir = join(process.cwd(), "app/banks/[bankId]/panels");

  it("declares a panel per view", () => {
    for (const file of [
      "bank-home-panel.tsx",
      "bank-profile-panel.tsx",
      "data-panel.tsx",
      "documents-panel.tsx",
      "entities-panel.tsx",
      "knowledge-panel.tsx",
      "recall-panel.tsx",
      "reflect-panel.tsx",
      "panel-state.tsx",
    ]) {
      expect(existsSync(join(panelsDir, file)), file).toBe(true);
    }
  });

  it("renders every view the shell can select", () => {
    const shell = readFileSync(join(process.cwd(), "app/banks/[bankId]/bank-detail-view.tsx"), "utf8");
    for (const view of ["home", "recall", "reflect", "data", "documents", "entities", "knowledge", "profile"]) {
      expect(shell, `view ${view} has no branch`).toContain(`initialState.view === "${view}"`);
    }
  });

  it("keeps the view vocabulary and its parameters in the shared module", () => {
    const source = readFileSync(join(process.cwd(), "lib/bank-detail.ts"), "utf8");
    for (const param of ["view", "subTab", "knowledgeTab", "bankConfigTab"]) {
      expect(source, `parameter ${param} missing`).toContain(`"${param}"`);
    }
    for (const value of ["world", "experience", "observations", "pages", "models"]) {
      expect(source, `value ${value} missing`).toContain(`"${value}"`);
    }
    for (const value of ["general", "memory-defense", "configuration", "webhooks", "audit-logs", "llm-requests"]) {
      expect(source, `settings tab ${value} missing`).toContain(`"${value}"`);
    }
  });

  it("loads each panel through the shared async helper", () => {
    for (const file of ["data-panel.tsx", "documents-panel.tsx", "entities-panel.tsx", "bank-home-panel.tsx"]) {
      const source = readFileSync(join(panelsDir, file), "utf8");
      expect(source, `${file} should use useAsyncData`).toContain("useAsyncData");
    }
  });

  it("covers every panel state so no panel renders blank on failure", () => {
    const state = readFileSync(join(panelsDir, "panel-state.tsx"), "utf8");
    for (const key of ["loadFailed", "retry", "empty"]) {
      expect(state, `panel state ${key} missing`).toContain(`t("${key}")`);
    }
  });
});
