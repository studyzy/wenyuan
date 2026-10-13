import { existsSync, readFileSync } from "node:fs";
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
describe("navigation shell", () => {
  const shellDir = join(process.cwd(), "components/shell");

  it("declares the shell components", () => {
    for (const file of [
      "app-shell.tsx",
      "bank-selector.tsx",
      "banks-overview.tsx",
      "language-switcher.tsx",
      "sidebar.tsx",
    ]) {
      expect(existsSync(join(shellDir, file)), file).toBe(true);
    }
  });

  it("drives the shell from the shared providers and the client endpoint layer", () => {
    const selector = readFileSync(join(shellDir, "bank-selector.tsx"), "utf8");
    expect(selector).toContain("useBank");
    expect(selector).toContain("banksApi");

    const overview = readFileSync(join(shellDir, "banks-overview.tsx"), "utf8");
    expect(overview).toContain("useBank");
    expect(overview).toContain("banksApi");

    const sidebar = readFileSync(join(shellDir, "sidebar.tsx"), "utf8");
    expect(sidebar).toContain("useBank");
    expect(sidebar).toContain("bankRoute");
  });

  it("never puts the locale in a URL path", () => {
    // The locale travels in a cookie, so a locale segment must not appear in
    // any user-visible route the shell builds.
    const switcher = readFileSync(join(shellDir, "language-switcher.tsx"), "utf8");
    expect(switcher).toContain("NEXT_LOCALE");
    expect(switcher).not.toMatch(/\/\$\{?locale\}?\//);
  });

  it("routes every bank link through the shared route builder", () => {
    for (const file of ["bank-selector.tsx", "banks-overview.tsx", "sidebar.tsx"]) {
      const source = readFileSync(join(shellDir, file), "utf8");
      if (source.includes("/banks/")) {
        expect(source, `${file} should use bankRoute`).toContain("bankRoute");
      }
    }
  });
});
