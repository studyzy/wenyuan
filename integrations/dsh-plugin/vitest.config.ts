import { defineConfig } from 'vitest/config'

/**
 * The unit lane: the Host half and the browser controller, run against the
 * package's own source without a prior build.
 */
export default defineConfig({
  test: {
    include: ['tests/**/*.spec.ts', 'tests/**/*.spec.tsx'],
    environment: 'node',
    // jsdom for the card, which React renders; the Host half ignores it.
    environmentMatchGlobs: [['tests/**/*.client.spec.tsx', 'jsdom']],
    css: true,
    server: {
      deps: {
        // Published client packages ship CSS modules; jsdom cannot import them
        // as raw files, so Vitest's pipeline (identity class maps) handles them.
        inline: [/@deepseek-ai\/dsh-client-.*/],
      },
    },
  },
})