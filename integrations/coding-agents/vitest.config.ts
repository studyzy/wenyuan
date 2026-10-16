import { defineConfig } from "vitest/config";

// One runner configuration for both suites. The scripts narrow it by path:
// `test` runs the offline unit suite, `test:e2e` builds first and then runs
// the end-to-end suite, which drives the real installer against a fake home.
export default defineConfig({
  test: {
    include: ["tests/**/*.test.ts"],
    environment: "node",
    // The fake-home fixture points location resolution at a temporary
    // directory through the environment, so suites must not run in parallel
    // inside one process over a shared environment.
    fileParallelism: false,
    restoreMocks: true,
  },
});
