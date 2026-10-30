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
    //
    // NODE_ENV=test is what makes React's development build load: React 19
    // only exports `act` in it, and @testing-library/react renders through
    // `act`. Without it every component test fails with "React.act is not a
    // function" even though the same suite passes where NODE_ENV is unset.
    pool: "forks",
    env: { TZ: "UTC", NODE_ENV: "test" },
  },
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./", import.meta.url)),
    },
  },
});