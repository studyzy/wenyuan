import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

/**
 * UI primitive coverage.
 *
 * The primitives are client components built on React, Radix, cmdk and Recharts.
 * To keep this suite runnable offline (no DOM runtime required) it verifies the
 * primitive surface structurally rather than rendering it. Rendering coverage
 * belongs to a browser-level stage.
 */
const UI_DIR = join(process.cwd(), "components/ui");
const CHARTS_DIR = join(process.cwd(), "components/charts");

describe("shared UI primitives", () => {
  it("declares the primitive modules", () => {
    const files = [
      "alert-dialog.tsx",
      "alert.tsx",
      "button.tsx",
      "card.tsx",
      "checkbox.tsx",
      "command.tsx",
      "dialog.tsx",
      "dropdown-menu.tsx",
      "facet-chip.tsx",
      "harness-logo.tsx",
      "index.ts",
      "inline-attachment-text.tsx",
      "input.tsx",
      "json-viewer.tsx",
      "label.tsx",
      "pagination.tsx",
      "popover.tsx",
      "select.tsx",
      "sonner.tsx",
      "spinner.tsx",
      "switch.tsx",
      "table.tsx",
      "tabs.tsx",
      "tag-list.tsx",
      "textarea.tsx",
    ];
    for (const file of files) {
      expect(existsSync(join(UI_DIR, file)), file).toBe(true);
    }
  });

  it("builds each interactive primitive on its Radix package", () => {
    const expectations: Record<string, string> = {
      "tabs.tsx": "@radix-ui/react-tabs",
      "dialog.tsx": "@radix-ui/react-dialog",
      "alert-dialog.tsx": "@radix-ui/react-alert-dialog",
      "dropdown-menu.tsx": "@radix-ui/react-dropdown-menu",
      "select.tsx": "@radix-ui/react-select",
      "popover.tsx": "@radix-ui/react-popover",
      "checkbox.tsx": "@radix-ui/react-checkbox",
      "switch.tsx": "@radix-ui/react-switch",
      "label.tsx": "@radix-ui/react-label",
      "button.tsx": "@radix-ui/react-slot",
    };
    for (const [file, pkg] of Object.entries(expectations)) {
      expect(readFileSync(join(UI_DIR, file), "utf8"), file).toContain(pkg);
    }
  });

  it("routes every primitive through the shared class-name join", () => {
    // A primitive that skips the join silently drops caller overrides.
    for (const file of [
      "button.tsx",
      "card.tsx",
      "input.tsx",
      "textarea.tsx",
      "label.tsx",
      "alert.tsx",
      "checkbox.tsx",
      "switch.tsx",
      "table.tsx",
      "tabs.tsx",
      "dialog.tsx",
      "select.tsx",
    ]) {
      const source = readFileSync(join(UI_DIR, file), "utf8");
      expect(source, `${file} should use the shared join`).toContain('from "../../lib/utils"');
    }
  });

  it("re-exports the tag list from the shared chip module", () => {
    expect(readFileSync(join(UI_DIR, "tag-list.tsx"), "utf8")).toContain("./facet-chip");
    expect(readFileSync(join(UI_DIR, "facet-chip.tsx"), "utf8")).toContain("export function TagList");
  });

  it("ships the command palette on cmdk and the toasts on sonner", () => {
    expect(readFileSync(join(UI_DIR, "command.tsx"), "utf8")).toContain("cmdk");
    expect(readFileSync(join(UI_DIR, "sonner.tsx"), "utf8")).toContain("sonner");
  });
});

describe("chart wrappers", () => {
  it("declares Recharts-based chart wrappers", () => {
    for (const file of ["memories-timeseries.tsx", "counts-bar.tsx", "index.ts"]) {
      expect(existsSync(join(CHARTS_DIR, file)), file).toBe(true);
    }
    expect(readFileSync(join(CHARTS_DIR, "memories-timeseries.tsx"), "utf8")).toContain("recharts");
  });
});