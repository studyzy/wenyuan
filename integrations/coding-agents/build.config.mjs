// Build driver for the package. esbuild has no configuration-file support, so
// this file is both the configuration and the script that runs it; `npm run
// build` invokes it directly after emitting declarations with tsc.
//
// Every entry point is bundled into one self-contained file with chunk
// splitting disabled: a host launches a packaged hook by absolute path and
// never resolves the rest of the package or a dependency tree.
import { chmod, mkdir } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)));

/** @type {{ entry: string; outfile: string; executable?: boolean }[]} */
const entryPoints = [
  { entry: "src/index.ts", outfile: "dist/index.mjs" },
  // The command executable, launched by a host through the bin entry or by an
  // operator in a terminal, so it carries the interpreter line too.
  {
    entry: "src/cli/main.ts",
    outfile: "dist/cli/main.mjs",
    executable: true,
  },
  {
    entry: "hooks/session-start.ts",
    outfile: "dist/runtime/hooks/session-start.mjs",
    executable: true,
  },
  {
    entry: "hooks/pre-prompt.ts",
    outfile: "dist/runtime/hooks/pre-prompt.mjs",
    executable: true,
  },
  {
    entry: "hooks/stop.ts",
    outfile: "dist/runtime/hooks/stop.mjs",
    executable: true,
  },
  // The MCP server a host launches over standard input/output. Its staged path
  // is what every host's MCP registration names, so it ships beside the hooks.
  {
    entry: "hooks/mcp-server.ts",
    outfile: "dist/runtime/mcp-server.mjs",
    executable: true,
  },
  // The deepen engine the background seed spawns beside the staged hook
  // bundles: core/seed.ts resolves it as dirname(hook bundle) + "deepen.mjs".
  {
    entry: "src/core/deepen.ts",
    outfile: "dist/runtime/hooks/deepen.mjs",
    executable: true,
  },
  {
    entry: "src/core/survey-supervisor.ts",
    outfile: "dist/runtime/hooks/survey-supervisor.mjs",
    executable: true,
  },
  // Persistent-plugin entry points. Each exports the adapter a host loads
  // in-process (package.json "exports" maps these paths), so they bundle like
  // the hooks: one self-contained file per host, no chunk splitting.
  ...["dsh", "cline", "kilo", "opencode", "opencode2", "pi", "prime-agent"].map(
    (name) => ({
      entry: `src/plugins/${name}.ts`,
      outfile: `dist/plugins/${name}.mjs`,
    })
  ),
  { entry: "src/plugins/index.ts", outfile: "dist/plugins/index.mjs" },
  // DSH loads its plugin from the STAGED runtime directory, not from the
  // package: the patch row it writes is a file:// URL under ~/.wenyuan/
  // wenyuan-memory, and staging copies dist/runtime wholesale. Emitting the
  // same bundle a second time under that tree is what makes the row's path
  // exist; without it the row points at a file staging never creates and DSH
  // silently skips the plugin.
  {
    entry: "src/plugins/dsh.ts",
    outfile: "dist/runtime/plugins/dsh.mjs",
  },
];

// A host execs a hook by path, so the bundled file carries its own
// interpreter line rather than relying on the launcher to supply one.
const shebang = "#!/usr/bin/env node";

for (const { entry, outfile, executable } of entryPoints) {
  await build({
    absWorkingDir: packageRoot,
    entryPoints: [entry],
    outfile,
    bundle: true,
    splitting: false,
    format: "esm",
    platform: "node",
    // The declared engines floor is higher; the bundle stays runnable on the
    // older runtimes a host may launch it with.
    target: "node18",
    sourcemap: false,
    banner: executable ? { js: shebang } : undefined,
    logLevel: "warning",
  });

  if (executable) {
    await chmod(join(packageRoot, outfile), 0o755);
  }
}

await mkdir(join(packageRoot, "dist", "runtime"), { recursive: true });
console.log(`built ${entryPoints.length} entry point(s)`);
