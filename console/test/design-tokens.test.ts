import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

/**
 * Guards the design-token foundation.
 *
 * A missing or mistyped token fails silently: the build succeeds, the class
 * emits nothing, and the component renders unstyled. These assertions fail
 * loudly instead.
 */
const css = readFileSync(new URL("../app/globals.css", import.meta.url), "utf8");

/** Names declared inside a `@theme inline { ... }` block, value stripped. */
function themeKeys(source: string): Set<string> {
  const block = source.match(/@theme\s+inline\s*\{([\s\S]*?)\n\}/);
  if (!block) return new Set();
  const keys = new Set<string>();
  for (const line of block[1].split("\n")) {
    const match = line.match(/^\s*(--[a-z0-9-]+)\s*:/i);
    if (match) keys.add(match[1]);
  }
  return keys;
}

/** Names declared inside a selector block such as `:root { ... }`. */
function rootKeys(source: string, selector: string): Set<string> {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const block = source.match(new RegExp(`${escaped}\\s*\\{([\\s\\S]*?)\\n\\}`));
  if (!block) return new Set();
  const keys = new Set<string>();
  for (const line of block[1].split("\n")) {
    const match = line.match(/^\s*(--[a-z0-9-]+)\s*:/i);
    if (match) keys.add(match[1]);
  }
  return keys;
}

describe("design-token foundation", () => {
  const theme = themeKeys(css);
  const light = rootKeys(css, ":root");
  const dark = rootKeys(css, ".dark");

  it("registers the theme map", () => {
    expect(theme.size).toBeGreaterThan(60);
  });

  it("resolves every generated color token to a declared variable", () => {
    const unresolved: string[] = [];
    for (const key of theme) {
      if (!key.startsWith("--color-")) continue;
      const block = css.match(/@theme\s+inline\s*\{([\s\S]*?)\n\}/);
      const line = block?.[1]
        .split("\n")
        .find((entry) => entry.trim().startsWith(`${key}:`));
      const target = line?.match(/var\((--[a-z0-9-]+)\)/i)?.[1];
      if (!target) continue;
      // A theme entry may point at another theme entry (traced) or at a raw
      // token; either way the name must be declared somewhere.
      if (!light.has(target) && !dark.has(target) && !theme.has(target)) {
        unresolved.push(`${key} -> ${target}`);
      }
    }
    expect(unresolved).toEqual([]);
  });

  it("declares the semantic color vocabulary the view components use", () => {
    const required = [
      "--color-card",
      "--color-foreground",
      "--color-border",
      "--color-primary",
      "--color-muted-foreground",
      "--color-chip-tag",
      "--color-sidebar-foreground",
      "--color-chart-1",
      "--color-surface-page",
      "--color-fg-muted",
      "--color-success",
      "--color-danger",
    ];
    for (const key of required) expect(theme.has(key), `${key} missing`).toBe(true);
  });

  it("rethemes the core tokens in dark mode", () => {
    for (const key of ["--background", "--card", "--foreground", "--border", "--primary"]) {
      expect(light.has(key), `${key} missing in :root`).toBe(true);
      expect(dark.has(key), `${key} missing in .dark`).toBe(true);
    }
    expect(light.has("--background") && dark.has("--background")).toBe(true);
  });

  it("binds the dark variant to a class rather than the OS setting", () => {
    expect(css).toContain("@custom-variant dark");
    expect(css).not.toMatch(/@media\s*\(prefers-color-scheme:\s*dark\)/);
  });

  it("registers the typography plugin for rendered markdown", () => {
    expect(css).toContain('@plugin "@tailwindcss/typography"');
  });

  it("defines the gradient utilities the design uses", () => {
    for (const rule of [".bg-primary-gradient", ".text-primary-gradient"]) {
      expect(css).toContain(rule);
    }
  });

  it("defines the app animations with a reduced-motion guard", () => {
    for (const name of [
      "logo-tumble",
      "logo-wiggle",
      "logo-flip",
      "logo-tumble-pulse",
      "list-row-enter",
      "soft-fade-in",
    ]) {
      expect(css, `@keyframes ${name} missing`).toContain(`@keyframes ${name}`);
    }
    expect(css).toContain("prefers-reduced-motion");
  });
});