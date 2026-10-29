import type { UserConfig } from 'tsdown'

/**
 * Single-package bundle: the Host plugin ESM entry plus the browser plugin
 * closure bundle for the DeepSeek Harness web client.
 *
 * - `tsc` emits declarations (and the Host runtime JS) to `lib/types/`.
 * - The Host pass bundles the runtime entry to `lib/index.js`; every declared
 *   dependency and peer stays external so the running harness resolves them
 *   from its own install (one instance of each service).
 * - The client pass bundles the browser half from `src/browser/index.ts` to
 *   `lib/client.js`: a closure-factory artifact that calls
 *   `window.__ModuleLoader__.load()` with the plugin id and resolves its
 *   externals through the injected require.
 *
 * The Host half imports nothing outside `node:` builtins, `@deepseek-ai/*`
 * peers and `zod`; the memory HTTP calls go through the runtime `fetch`, so
 * there is no vendor client to bundle.
 */

const HOST_EXTERNALS: readonly string[] = [
  '@deepseek-ai/cordis',
  '@deepseek-ai/schemastery',
  '@deepseek-ai/dsh-agent',
  '@deepseek-ai/dsh-llm',
  '@deepseek-ai/dsh-session',
  '@deepseek-ai/dsh-session-projection',
  '@deepseek-ai/dsh-tools',
  '@deepseek-ai/dsh-credentials',
  'zod',
]

const CLIENT_ID = '@studyzy/dsh-wenyuan-memory/client'

/**
 * Client-bundle externals. A fetch bundle may only require the shell's
 * platform-singleton seed words; requiring anything else misses the frozen
 * module table and throws at load. The seed table is `PLATFORM_MODULES` in the
 * harness's `dsh-client-web` package.
 *
 * The `dsh-client-*` packages this plugin consumes for TYPES only
 * (ui-settings, ui-slots, locale) are deliberately absent: those imports are
 * erased at build, and listing them would emit a require the module table
 * cannot answer.
 */
const CLIENT_EXTERNALS: readonly string[] = [
  'react',
  'react/jsx-runtime',
  'react-dom',
  'react-dom/client',
  '@deepseek-ai/cordis',
  '@deepseek-ai/dsh-client-store',
  '@deepseek-ai/dsh-client-ui-slots',
  '@deepseek-ai/dsh-client-ui-primitives',
  '@deepseek-ai/dsh-client-ui-dockkit',
]

const host: UserConfig = {
  name: '@studyzy/dsh-wenyuan-memory',
  entry: ['lib/types/index.js'],
  outDir: 'lib',
  format: 'esm',
  platform: 'node',
  target: 'es2024',
  fixedExtension: false,
  dts: false,
  clean: false,
  external: [...HOST_EXTERNALS],
}

const client: UserConfig = {
  name: CLIENT_ID,
  entry: { client: 'src/browser/index.ts' },
  outDir: 'lib',
  format: 'cjs',
  platform: 'browser',
  target: 'es2024',
  dts: false,
  sourcemap: true,
  clean: false,
  external: [...CLIENT_EXTERNALS],
  noExternal: (id: string) => (CLIENT_EXTERNALS.includes(id) ? undefined : true),
  // Substitutes the Node idiom `process.env.NODE_ENV` that inlined deps
  // (react/jsx-runtime guards) reference; the browser has no `process`.
  define: {
    'process.env.NODE_ENV': JSON.stringify(process.env.NODE_ENV ?? 'production'),
  },
  outputOptions: {
    entryFileNames: 'client.js',
    banner: `window.__ModuleLoader__.load({ id: ${JSON.stringify(CLIENT_ID)}, factory: (require) => {`,
    footer: 'return module.exports; } });',
    intro: 'var module = { exports: {} }; var exports = module.exports;',
  },
}

export default [host, client]