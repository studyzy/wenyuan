import { chmod, mkdir, mkdtemp, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { delimiter, dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

/**
 * The shared test fixture: a temporary fake home.
 *
 * Per-user location resolution is redirected purely through the environment
 * variables the installer already reads, so no new public name is introduced
 * and no installer code path knows it is under test. Nothing here mocks an
 * installer module: the helper drives the real built entry point in-process.
 */

export const DEPLOYMENT_ENV_VARS = [
  "WENYUAN_CONFIG",
  "WENYUAN_API_URL",
  "WENYUAN_API_TOKEN",
  "WENYUAN_BANK_ID",
  "WENYUAN_API_PORT",
] as const;

export interface FakeHomeOptions {
  /** Files to seed, keyed by their path relative to the home directory. */
  files?: Record<string, string>;
  /** Extra environment for the run, merged over the redirected home. */
  environment?: Record<string, string>;
}

/** A parsed host config: the shape every suite reads them back into. */
export type JsonDocument = Record<string, unknown>;

export interface FakeHome {
  readonly home: string;
  readonly packageRoot: string;
  /** A fresh environment pointing every per-user location at the fake home. */
  env(): NodeJS.ProcessEnv;
  /** Applies that environment and returns the function that restores it. */
  apply(): () => void;
  /** Replaces the PATH every later `env()` hands out, once a directory exists. */
  overridePath(path: string): void;
  /** True when the suite asked for the host stand-in PATH. */
  wantsStandInPath(): boolean;
  path(...segments: string[]): string;
  write(relativePath: string, content: string): Promise<void>;
  read(relativePath: string): Promise<string>;
  /** Reads a plain-JSON host config; a file that permits comments is read as text. */
  readJson(relativePath: string): Promise<JsonDocument>;
  exists(relativePath: string): Promise<boolean>;
  isFile(relativePath: string): Promise<boolean>;
  list(relativePath?: string): Promise<string[]>;
  /** Every file below a directory, not the directories themselves. */
  listFiles(relativePath?: string): Promise<string[]>;
  cleanup(): Promise<void>;
}

/** The package root the real installer is invoked from. */
export function packageRoot(): string {
  return resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");
}

export async function createFakeHome(options: FakeHomeOptions = {}): Promise<FakeHome> {
  const home: string = await mkdtemp(join(tmpdir(), "wenyuan-fake-home-"));
  const root: string = packageRoot();
  const requested: NodeJS.ProcessEnv = { ...options.environment };
  // The stand-ins live beside the fake home, not inside it: a suite's file
  // listing must show only what the installer wrote into the host's home.
  const bin: string = await mkdtemp(join(tmpdir(), "wenyuan-stand-ins-"));

  const fixture: FakeHome = {
    home,
    packageRoot: root,
    env(): NodeJS.ProcessEnv {
      const environment: NodeJS.ProcessEnv = { ...process.env };
      environment.HOME = home;
      environment.USERPROFILE = home;
      for (const name of DEPLOYMENT_ENV_VARS) {
        delete environment[name];
      }
      return Object.assign(environment, requested);
    },
    wantsStandInPath(): boolean {
      return requested.PATH === NO_HOST_CLI;
    },
    overridePath(path: string): void {
      requested.PATH = path;
    },
    apply(): () => void {
      const saved: NodeJS.ProcessEnv = { ...process.env };
      const applied: NodeJS.ProcessEnv = fixture.env();
      for (const name of Object.keys(process.env)) {
        if (!(name in applied)) {
          delete process.env[name];
        }
      }
      Object.assign(process.env, applied);
      return () => {
        for (const name of Object.keys(process.env)) {
          if (!(name in saved)) {
            delete process.env[name];
          }
        }
        Object.assign(process.env, saved);
      };
    },
    path(...segments: string[]): string {
      return join(home, ...segments);
    },
    async write(relativePath: string, content: string): Promise<void> {
      const target: string = join(home, relativePath);
      await mkdir(dirname(target), { recursive: true });
      await writeFile(target, content, "utf8");
    },
    async read(relativePath: string): Promise<string> {
      return readFile(join(home, relativePath), "utf8");
    },
    async readJson(relativePath: string): Promise<JsonDocument> {
      const source: string = await readFile(join(home, relativePath), "utf8");
      const parsed: unknown = JSON.parse(source);
      return parsed !== null && typeof parsed === "object" && !Array.isArray(parsed)
        ? (parsed as JsonDocument)
        : {};
    },
    async exists(relativePath: string): Promise<boolean> {
      try {
        await stat(join(home, relativePath));
        return true;
      } catch {
        return false;
      }
    },
    async isFile(relativePath: string): Promise<boolean> {
      try {
        return (await stat(join(home, relativePath))).isFile();
      } catch {
        return false;
      }
    },
    async list(relativePath = "."): Promise<string[]> {
      const base: string = join(home, relativePath);
      const found: string[] = [];
      async function walk(current: string, prefix: string): Promise<void> {
        let entries: { name: string; isDirectory(): boolean }[];
        try {
          entries = await readdir(current, { withFileTypes: true });
        } catch {
          return;
        }
        for (const entry of entries) {
          const name: string = prefix === "" ? entry.name : `${prefix}/${entry.name}`;
          found.push(name);
          if (entry.isDirectory()) {
            await walk(join(current, entry.name), name);
          }
        }
      }
      await walk(base, "");
      return found.sort();
    },
    async listFiles(relativePath = "."): Promise<string[]> {
      const prefix: string = relativePath === "." ? "" : `${relativePath}/`;
      const files: string[] = [];
      for (const entry of await fixture.list(relativePath)) {
        if (await fixture.isFile(`${prefix}${entry}`)) {
          files.push(`${prefix}${entry}`);
        }
      }
      return files;
    },
    async cleanup(): Promise<void> {
      await rm(home, { recursive: true, force: true });
      await rm(bin, { recursive: true, force: true }).catch(() => undefined);
    },
  };

  for (const [relativePath, content] of Object.entries(options.files ?? {})) {
    await fixture.write(relativePath, content);
  }
  // Every home gets its host stand-ins, and a PATH that reaches them. A suite
  // that wants the host CLIs answers "wired"; one that writes its own PATH or
  // its own stand-ins overrides this.
  // A suite that wants no host CLI on PATH at all (so detection sees only
  // config directories) asks for that explicitly.
  // A suite may state its own PATH to keep host CLIs out of reach; otherwise
  // every home gets the stand-ins and a PATH that reaches them.
  // Every home gets the stand-ins; `useHostClis` decides whether to adopt the
  // stand-in PATH or keep the one a suite named.
  await useHostClis(fixture, bin);
  return fixture;
}

/** Creates a fake home, hands it to the suite and cleans it up afterwards. */
export async function withFakeHome<T>(
  options: FakeHomeOptions,
  body: (home: FakeHome) => Promise<T>,
): Promise<T> {
  const fake: FakeHome = await createFakeHome(options);
  const restore: () => void = fake.apply();
  try {
    return await body(fake);
  } finally {
    restore();
    await fake.cleanup();
  }
}

export interface InstallerRun {
  status: number;
  stdout: string;
  stderr: string;
  /** Both channels in the order they were written, for assertions that do not care. */
  combined(): string;
}

/** The PATH directory the stand-ins below live in, relative to the fake home. */
export const STAND_IN_BIN = "bin";

/**
 * The PATH a suite runs the installer under: the host stand-ins, and nothing
 * else.
 *
 * Host detection resolves through PATH, and so does every host CLI the package
 * drives — so a suite that asserts "wired" needs those CLIs to answer, and a
 * suite must never reach a real `claude`/`codex` installed on the developer's
 * machine. Both halves are met by running under this one directory.
 *
 * A suite writes `PATH: NO_HOST_CLI` and `createFakeHome` replaces it with the
 * stand-ins' real directory once it has written them, so the value survives the
 * gap where the directory does not exist yet.
 */
export const NO_HOST_CLI = "__wenyuan-host-stand-ins__";

/**
 * The CLIs this package drives, each a recording stand-in.
 *
 * - `claude` answers `mcp get` with what it holds under the server's name and
 *   records every invocation (`claude-calls.log`), so a suite can assert both
 *   the argv the installer passed and the ownership decision it made.
 * - `codex`, `cursor`, `copilot`, `codebuddy` are detection probes: the
 *   installer resolves a host by finding its executable or its config directory.
 */
const HOST_CLIS: readonly string[] = ["claude", "codex", "cursor", "copilot", "codebuddy", "dsh"];

/**
 * Writes the host stand-ins into the fake home's PATH directory.
 * @returns The PATH to run a suite under.
 */
/**
 * Makes a home's host stand-ins available, and gives it the PATH that reaches
 * them. `createFakeHome` calls this, so every suite gets it without asking.
 */
export async function useHostClis(home: FakeHome, bin?: string): Promise<string> {
  const directory: string = bin ?? join(home.home, STAND_IN_BIN);
  await writeHostClisAt(directory);
  // A suite that asked for the stand-in PATH gets the real directory now that
  // it exists; one that named its own PATH keeps it.
  if (home.wantsStandInPath()) {
    home.overridePath(directory);
  }
  return directory;
}

export async function writeHostClis(home: FakeHome): Promise<string> {
  const bin: string = join(home.home, STAND_IN_BIN);
  await writeHostClisAt(bin);
  return bin;
}

/** Writes the stand-ins into one directory; split out so the fixture can call it. */
async function writeHostClisAt(bin: string): Promise<void> {
  await mkdir(bin, { recursive: true });
  for (const name of HOST_CLIS) {
    await writeFile(join(bin, name), hostStandIn(name), "utf8");
    await chmod(join(bin, name), 0o755);
  }
}

/**
 * One stand-in's script. It is `/bin/sh` with built-ins only: it runs under a
 * PATH that holds nothing but the stand-ins themselves.
 */
function hostStandIn(name: string): string {
  // The log records the server definition itself once one is added, so `mcp
  // get` can answer with what was registered — the same information the real
  // CLI holds in its own store.
  return [
    "#!/bin/sh",
    `here="\${0%/*}"`,
    `echo "$*" >> "$here/${name}-calls.log"`,
    'if [ "$1" = "mcp" ] && [ "$2" = "get" ]; then',
    `  if [ -s "$here/${name}-added" ]; then`,
    `    echo "wenyuan-memory:"`,
    `    echo "  Command: node"`,
    // `read` is a shell built-in, so the definition comes back under the
    // restricted PATH a suite runs with — `cat` would not be found there.
    `    read -r line < "$here/${name}-added"`,
    `    echo "  Args: $line"`,
    "    exit 0",
    "  fi",
    "  exit 1",
    "fi",
    'if [ "$1" = "mcp" ] && [ "$2" = "add" ]; then',
    '  shift 2',
    '  for arg in "$@"; do',
    '    case "$arg" in',
    '      /dev/null|command|node) continue ;;',
    '      /*) echo "$arg" > "$here/NAME-added"; break ;;',
    '    esac',
    '  done',
    "fi",
    'if [ "$1" = "mcp" ] && [ "$2" = "remove" ]; then',
    // A shell redirection rather than `rm`, again because PATH holds only the
    // stand-ins: the real CLI drops the entry, and an empty definition file is
    // an entry that is gone.
    `  : > "$here/${name}-added"`,
    "fi",
    "exit 0",
    "",
  ].join("\n").replaceAll("NAME-added", `${name}-added`);
}

/**
 * A recording stand-in for the `dsh` executable.
 *
 * Installing DSH adds the memory plugin through DSH's own `dsh plugin` command,
 * so a suite that wires DSH needs that executable on PATH. The stand-in appends
 * its arguments to `bin/dsh-calls.log` and exits with `exitCode`. It locates its
 * log relative to `$0` with shell built-ins only, because the restricted PATH it
 * runs under holds no other command.
 * @returns The directory to put on PATH.
 */
export async function writeStandInDsh(home: FakeHome, exitCode = 0): Promise<string> {
  const script: string = [
    "#!/bin/sh",
    `log="\${0%/*}/dsh-calls.log"`,
    `echo "$*" >> "$log"`,
    `exit ${exitCode}`,
    "",
  ].join("\n");
  await home.write(`${STAND_IN_BIN}/dsh`, script);
  await chmod(join(home.home, STAND_IN_BIN, "dsh"), 0o755);
  return join(home.home, STAND_IN_BIN);
}

/**
 * Runs `body` with PATH set to exactly `directories`.
 *
 * A suite that wires a host through that host's own CLI needs this; nothing in
 * the installer reads PATH beyond finding that CLI.
 */
export async function withRestrictedPath<T>(directories: readonly string[], body: () => Promise<T>): Promise<T> {
  const saved: string | undefined = process.env.PATH;
  process.env.PATH = directories.join(delimiter);
  try {
    return await body();
  } finally {
    if (saved === undefined) {
      delete process.env.PATH;
    } else {
      process.env.PATH = saved;
    }
  }
}

export interface RunInstallerOptions {
  /** The package location to run from; defaults to this package's own build. */
  packageRoot?: string;
  environment?: Record<string, string>;
}

/**
 * Drives the real installer entry point in-process and captures its exit
 * status and output.
 *
 * Contract for the entry point: `dist/cli/main.mjs` exports
 * `run(argv: string[]): Promise<number>`. The build has to have run, which the
 * end-to-end script does before it invokes any suite.
 */
export async function runInstaller(
  argv: readonly string[],
  options: RunInstallerOptions = {},
): Promise<InstallerRun> {
  const root: string = options.packageRoot ?? packageRoot();
  const entry: string = join(root, "dist", "cli", "main.mjs");
  const module: unknown = await import(pathToFileURL(entry).href);
  const run = (module as { run?: (args: string[]) => Promise<number> | number }).run;
  if (typeof run !== "function") {
    throw new Error(`${entry} does not export run(argv): the installer entry point is missing`);
  }
  return captureOutput(async () => run([...argv]));
}

/**
 * Runs a body with both output channels captured, and returns what it printed
 * together with the status it returned.
 */
export async function captureOutput(body: () => Promise<number>): Promise<InstallerRun> {
  let stdout = "";
  let stderr = "";
  const originalOut = process.stdout.write.bind(process.stdout);
  const originalErr = process.stderr.write.bind(process.stderr);
  process.stdout.write = ((chunk: string | Uint8Array): boolean => {
    stdout += String(chunk);
    return true;
  }) as typeof process.stdout.write;
  process.stderr.write = ((chunk: string | Uint8Array): boolean => {
    stderr += String(chunk);
    return true;
  }) as typeof process.stderr.write;
  try {
    const status: number = await body();
    return { status, stdout, stderr, combined: () => `${stdout}${stderr}` };
  } finally {
    process.stdout.write = originalOut;
    process.stderr.write = originalErr;
  }
}
