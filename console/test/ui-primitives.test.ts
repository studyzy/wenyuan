import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

/**
 * UI primitive coverage.
 *
 * The primitives are client components built on React, Radix and Recharts. To
 * keep this suite runnable offline (no framework runtime required) it verifies
 * the primitive surface structurally rather than rendering. Rendering coverage
 * is deferred to the browser-level review stage.
 */
const UI_DIR = join(process.cwd(), "components/ui");
const CHARTS_DIR = join(process.cwd(), "components/charts");

describe("shared UI primitives", () => {
  it("declares the primitive modules", () => {
    for (const file of ["button.tsx", "card.tsx", "tabs.tsx", "modal.tsx", "index.ts"]) {
      expect(existsSync(join(UI_DIR, file)), file).toBe(true);
    }
  });

  it("builds the tab and modal primitives on Radix", () => {
    expect(readFileSync(join(UI_DIR, "tabs.tsx"), "utf8")).toContain("@radix-ui/react-tabs");
    expect(readFileSync(join(UI_DIR, "modal.tsx"), "utf8")).toContain("@radix-ui/react-dialog");
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