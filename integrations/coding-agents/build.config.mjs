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
