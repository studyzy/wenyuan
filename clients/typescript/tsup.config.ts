import { defineConfig } from "tsup";

// The published artifact is bundled, not a raw `tsc` tree: the wrapper deep-imports
// the generated directory, and Node's ESM loader refuses extensionless directory
// specifiers, so `tsc`'s emitted tree would not be importable. tsup inlines the
// generated sources into one entry point per module system.
export default defineConfig({
  entry: { index: "src/index.ts" },
  outDir: "dist",
  format: ["esm", "cjs"],
  dts: true,
  sourcemap: true,
  clean: true,
  target: "node18",
  outExtension({ format }) {
    return { js: format === "cjs" ? ".cjs" : ".js" };
  },
});