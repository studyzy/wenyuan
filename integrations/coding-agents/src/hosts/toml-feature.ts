import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname } from "node:path";
import { backupExistingFile } from "../write/backup";

/**
 * A host feature switch kept in a TOML config file.
 *
 * Codex ships its hook runner off: a hook registered in `~/.codex/hooks.json`
 * runs only when `~/.codex/config.toml` states `hooks = true`. Codex ≥ 0.145
 * reads that under `[features]` and older builds as a top-level `codex_hooks`,
 * so either spelling that is already true counts as enabled and is left alone.
 *
 * The edit is a marker-free text edit on purpose: it touches only the one key,
 * so an operator's comments, ordering and formatting survive — the same rule
 * the MCP table edit follows.
 */

/** The spellings a host may use to state the feature is already on. */
const ENABLED_SPELLINGS: readonly RegExp[] = [
  /^\s*(features\.)?hooks\s*=\s*true\b/m,
  /^\s*codex_hooks\s*=\s*true\b/m,
];

/**
 * Ensures the feature is on, and says so. An existing `true` in either spelling
 * is respected; an existing `false` is flipped, because a hook this package just
 * registered cannot run otherwise and a silent no-op would be worse.
 */
export async function ensureTomlFeature(configPath: string, feature: string): Promise<string[]> {
  const before: string | null = await readSource(configPath);
  const source: string = before ?? "";
  if (ENABLED_SPELLINGS.some((pattern) => pattern.test(source))) {
    return [`${feature} hooks are enabled in ${configPath}`];
  }
  const flipped: string | null = flipFalseValue(source, feature);
  if (flipped !== null) {
    await write(configPath, before, flipped);
    return [`set ${feature} = true in ${configPath} (it was false; the hooks cannot run otherwise)`];
  }
  await write(configPath, before, addFeature(source, feature));
  return [`added [features] ${feature} = true to ${configPath}`];
}

/** Turns an existing `<feature> = false` into `true`, wherever it is written. */
function flipFalseValue(source: string, feature: string): string | null {
  const pattern = new RegExp(`^([ \t]*(?:features\\.)?${escape(feature)}[ \t]*=[ \t]*)false[ \t]*$`, "m");
  if (!pattern.test(source)) {
    return null;
  }
  return source.replace(pattern, "$1true");
}

/**
 * Adds the feature. It is appended as a `features.<name>` key rather than as a
 * new `[features]` table: a second table of that name is a TOML error, and the
 * dotted spelling is valid at any position in the document.
 */
function addFeature(source: string, feature: string): string {
  const body: string = source.replace(/\s*$/, "");
  const line: string = `features.${feature} = true`;
  return body === "" ? `${line}\n` : `${body}\n${line}\n`;
}

async function write(configPath: string, before: string | null, text: string): Promise<void> {
  if (before !== null) {
    await backupExistingFile(configPath);
  }
  await mkdir(dirname(configPath), { recursive: true });
  await writeFile(configPath, text, { mode: 0o600 });
}

async function readSource(path: string): Promise<string | null> {
  try {
    return await readFile(path, "utf8");
  } catch {
    return null;
  }
}

function escape(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}