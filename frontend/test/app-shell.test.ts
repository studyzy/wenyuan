import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { resolveTheme, themeBootstrapScript, THEME_STORAGE_KEY } from "../app/providers/theme-bootstrap";

function read(relativePath: string): string {
  return readFileSync(join(process.cwd(), relativePath), "utf8");
}

describe("application shell", () => {
  it("renders the pre-paint theme bootstrap consistent with the theme resolver", () => {
    const script = themeBootstrapScript();
    expect(script).toContain(THEME_STORAGE_KEY);
    expect(script).toContain("document.documentElement");
    expect(resolveTheme("dark")).toBe("dark");
    expect(resolveTheme("light")).toBe("light");
    expect(resolveTheme(null)).toBe("light");
  });

  it("keeps the root layout a pass-through with no document element", () => {
    const layout = read("app/layout.tsx");
    expect(layout).toContain('"./globals.css"');
    // A pass-through returns its children; the document element lives in the
    // locale layout (asserted below), so this file renders no <html>/<body>.
    expect(layout).toContain("return children");
  });

  it("declares the document root in the locale layout", () => {
    const layout = read("app/[locale]/layout.tsx");
    expect(layout).toContain("lang={locale}");
    expect(layout).toContain("<body");
    expect(layout).toContain("ThemeProvider");
    expect(layout).toContain("FeaturesProvider");
    expect(layout).toContain("BankProvider");
    expect(layout).toContain("NextIntlClientProvider");
    expect(layout).toContain("themeBootstrapScript");
    expect(layout).toContain("generateStaticParams");
    expect(layout).toContain("hasLocale");
  });

  it("declares the frontend package with its build entrypoints", () => {
    const pkg = JSON.parse(read("package.json")) as {
      scripts: Record<string, string>;
      license: string;
    };
    for (const script of ["dev", "build", "start", "test", "lint", "typecheck"]) {
      expect(pkg.scripts[script], script).toBeTruthy();
    }
    expect(pkg.license).toBe("ISC");
  });

  it("declares the startup instrumentation entrypoint", () => {
    const source = read("instrumentation.ts");
    expect(source).toContain("HINDSIGHT_CP_DATAPLANE_API_URL");
    expect(source).toContain("register");
  });
});

describe("navigation shell", () => {
  const shellComponents = [
    "bank-selector.tsx",
    "banks-overview.tsx",
    "language-switcher.tsx",
    "sidebar.tsx",
  ];

  it("declares the shell components", () => {
    for (const file of shellComponents) {
      expect(existsSync(join(process.cwd(), "components", file)), file).toBe(true);
    }
  });

  it("drives the shell from the shared bank context and the route builder", () => {
    for (const file of shellComponents) {
      const source = read(join("components", file));
      if (file === "language-switcher.tsx") continue;
      expect(source, `${file} should read the bank context`).toContain("useBank");
    }
    for (const file of ["bank-selector.tsx", "banks-overview.tsx", "sidebar.tsx"]) {
      const source = read(join("components", file));
      expect(source, `${file} should use bankRoute`).toContain("bankRoute");
    }
  });

  it("never puts the locale in a URL path", () => {
    // The locale travels in the NEXT_LOCALE cookie, resolved by next-intl under
    // localePrefix "never"; the switcher navigates through next-intl's own
    // router so no locale segment is ever hand-built into a URL.
    const switcher = read("components/language-switcher.tsx");
    expect(switcher).toContain("@/i18n/navigation");
    expect(switcher).not.toMatch(/\/\$\{?locale\}?\//);
  });
});