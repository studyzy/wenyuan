import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { resolveTheme, themeBootstrapScript, THEME_STORAGE_KEY } from "../app/providers/theme-bootstrap";

describe("application shell", () => {
  it("renders the pre-paint theme bootstrap consistent with the theme resolver", () => {
    const script = themeBootstrapScript();
    expect(script).toContain(THEME_STORAGE_KEY);
    expect(script).toContain("document.documentElement");
    expect(resolveTheme("dark")).toBe("dark");
    expect(resolveTheme("light")).toBe("light");
    expect(resolveTheme(null)).toBe("light");
  });

  it("declares the document root with a locale lang attribute and providers", () => {
    const layout = readFileSync(join(process.cwd(), "app/layout.tsx"), "utf8");
    expect(layout).toContain("lang={locale}");
    expect(layout).toContain("Providers");
    expect(layout).toContain("NextIntlClientProvider");
    expect(layout).toContain("themeBootstrapScript");
  });

  it("declares the console package with its build entrypoints", () => {
    const pkg = JSON.parse(readFileSync(join(process.cwd(), "package.json"), "utf8")) as {
      scripts: Record<string, string>;
      license: string;
    };
    for (const script of ["dev", "build", "start", "test", "lint", "typecheck"]) {
      expect(pkg.scripts[script], script).toBeTruthy();
    }
    expect(pkg.license).toBe("ISC");
  });
});