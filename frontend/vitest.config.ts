import { defineConfig } from "vitest/config";
import { fileURLToPath } from "node:url";

export default defineConfig({
  test: {
    environment: "node",
    include: ["test/**/*.test.ts", "tests/**/*.test.ts", "tests/**/*.test.tsx"],
    globals: true,
    // The copied suite pins watermarks against the clock and flips TZ at
    // runtime for its daylight-saving case; a child process per file keeps the
    // runtime TZ change effective, and UTC is the baseline the fixtures assume.
    pool: "forks",
    env: { TZ: "UTC" },
  },
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./", import.meta.url)),
    },
  },
});