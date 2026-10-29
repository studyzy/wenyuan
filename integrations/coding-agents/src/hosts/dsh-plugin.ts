import { spawn } from "node:child_process";
import { readdir, stat } from "node:fs/promises";
import { join } from "node:path";

/**
 * Installing the DSH memory plugin into a profile.
 *
 * DSH reaches WenYuan through the Cordis bundle `@studyzy/dsh-wenyuan-memory`
 * (`integrations/dsh-plugin`), which owns recall, retain and the memory tools
 * in-process. A bundle belongs to one profile, so the published package is
 * added with DSH's own forwarder — `dsh plugin --profile <name> add <spec>` —
 * which initializes a missing profile, runs pnpm in it, and reconciles
 * `dsh.profile.bundles` afterwards. Doing that through the real CLI rather than
 * editing the manifest keeps DSH's own activation rule the only one.
 *
 * `dsh` must therefore be on PATH; a profile is never written directly.
 */

/** The published package, as `pnpm add` expects it. */
export const DSH_PLUGIN_PACKAGE = "@studyzy/dsh-wenyuan-memory";

export type DshPluginResult =
  | { ok: true; messages: string[] }
  /** `missingExecutable`: DSH is not installed, so there is no profile to add to. */
  | { ok: false; messages: string[]; missingExecutable?: boolean };

interface RunResult {
  code: number | null;
  output: string;
}

function run(command: string, args: readonly string[]): Promise<RunResult> {
  return new Promise((resolve) => {
    const child = spawn(command, [...args], { stdio: ["ignore", "pipe", "pipe"] });
    let output = "";
    child.stdout.on("data", (chunk: Buffer) => {
      output += chunk.toString();
    });
    child.stderr.on("data", (chunk: Buffer) => {
      output += chunk.toString();
    });
    child.on("error", (cause: NodeJS.ErrnoException) => {
      // ENOENT is the one refusal with a remedy the operator can act on.
      resolve({ code: null, output: `cannot run ${command}: ${cause.code ?? String(cause)}` });
    });
    child.on("close", (code: number | null) => {
      resolve({ code, output });
    });
  });
}

/** The last few lines of a failed run, which is where pnpm and DSH state the cause. */
function tail(output: string, lines = 5): string {
  const kept: string[] = output
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line.length > 0)
    .slice(-lines);
  return kept.join(" | ");
}

export async function installDshPlugin(profile: string): Promise<DshPluginResult> {
  const result: RunResult = await run("dsh", ["plugin", "--profile", profile, "add", DSH_PLUGIN_PACKAGE]);
  if (result.code === 0) {
    return { ok: true, messages: [`added ${DSH_PLUGIN_PACKAGE} to profile ${profile}`] };
  }
  const detail: string = tail(result.output);
  const reason: string =
    result.code === null
      ? "the dsh executable is not on PATH, so no DSH profile could be found or initialized"
      : `\`dsh plugin --profile ${profile} add ${DSH_PLUGIN_PACKAGE}\` failed (exit ${result.code ?? "signal"}, pnpm must be on PATH)${
          detail.length > 0 ? `: ${detail}` : ""
        }`;
  return { ok: false, missingExecutable: result.code === null, messages: [reason] };
}

/** The profiles this machine already runs: `$DSH_HOME/profiles/<name>`. */
export interface DshProfile {
  name: string;
  directory: string;
}

/**
 * The profiles to install into.
 *
 * An existing directory with a manifest is a profile DSH has already booted, so
 * it is installed into; a missing `package.json` or a reserved `desktop` profile
 * (which the Electron application owns) is left alone. A machine with no profile
 * at all gets the `web` template, which is what `dsh web` boots.
 */
export async function dshProfiles(profilesDirectory: string, fallback: string): Promise<DshProfile[]> {
  let entries: string[];
  try {
    entries = await readdir(profilesDirectory);
  } catch {
    return [{ name: fallback, directory: join(profilesDirectory, fallback) }];
  }
  const found: DshProfile[] = [];
  for (const name of entries.sort()) {
    if (name.startsWith(".") || name === "desktop" || name === "node_modules") {
      continue;
    }
    const directory: string = join(profilesDirectory, name);
    if (await isFile(join(directory, "package.json"))) {
      found.push({ name, directory });
    }
  }
  return found.length > 0 ? found : [{ name: fallback, directory: join(profilesDirectory, fallback) }];
}

async function isFile(path: string): Promise<boolean> {
  return stat(path).then(
    (info) => info.isFile(),
    () => false,
  );
}