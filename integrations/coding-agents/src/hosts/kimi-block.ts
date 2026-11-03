import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { backupExistingFile } from "../write/backup";
import { allRegistrations } from "./lifecycle";

/**
 * Kimi Code's hook block: a marker-delimited run of TOML tables inside the
 * host's own config.toml.
 *
 * Kimi validates every [[hooks]] entry against a strict key set - the event,
 * the command, the timeout - and one unknown key does not drop that entry,
 * it drops EVERY hook in the file, at warning severity only. Neither JSON
 * registration shape can express the flat array, and a document round-trip
 * would reflow a config the user maintains by hand, so the block is written
 * as marked text: install replaces the previous block, uninstall strips it,
 * and everything outside the markers stays byte for byte the user's.
 */

export const KIMI_MARKER_START = "# WENYUAN_CODING_AGENTS_KIMI_START";
export const KIMI_MARKER_END = "# WENYUAN_CODING_AGENTS_KIMI_END";

/** The marker-delimited block, matched across line boundaries. */
export const KIMI_BLOCK_RE = new RegExp(
  `\\n?${KIMI_MARKER_START}[\\s\\S]*?${KIMI_MARKER_END}\\n?`,
  "g",
);

/** The command line one registration runs: the launcher and the quoted script. */
function entryCommand(scriptPath: string): string {
  return JSON.stringify(`node "${scriptPath}"`);
}

/**
 * Renders the block from the shared lifecycle declaration, so the installed
 * hooks can never diverge from the ones the packaged entry points serve. The
 * timeout is the host's declared seconds budget, and each entry carries
 * exactly the keys Kimi's validator accepts.
 */
export function renderKimiBlock(
  hostId: string,
  scriptPathOf: (scriptFile: string) => string,
): string {
  const entries: string[] = allRegistrations(hostId).map(
    (registration) =>
      `[[hooks]]\nevent = ${JSON.stringify(registration.event)}\n` +
      `command = ${entryCommand(scriptPathOf(registration.scriptFile))}\n` +
      `timeout = ${registration.budget}\n`,
  );
  return `\n${KIMI_MARKER_START}\n${entries.join("\n")}${KIMI_MARKER_END}\n`;
}

/**
 * Replaces any previous block and appends the current one. A re-install after
 * the package moved must repair the now-dead paths, which is what replacing -
 * rather than skipping when a block exists - buys.
 */
export async function installKimiBlock(configPath: string, block: string): Promise<boolean> {
  const existing: string | null = await readOrNull(configPath);
  const withoutOurs: string = existing === null ? "\n" : existing.replace(KIMI_BLOCK_RE, "\n");
  const next: string = `${withoutOurs.replace(/\n*$/, "\n")}${block}`;
  if (next === existing) {
    return false;
  }
  if (existing !== null) {
    await backupExistingFile(configPath);
  }
  await mkdir(dirname(configPath), { recursive: true });
  await writeFile(configPath, next, { mode: 0o600 });
  return true;
}

/** Strips the block, leaving the rest of the config byte for byte intact. */
export async function removeKimiBlock(configPath: string): Promise<boolean> {
  const existing: string | null = await readOrNull(configPath);
  if (existing === null) {
    return false;
  }
  const cleaned: string = existing.replace(KIMI_BLOCK_RE, "\n");
  if (cleaned === existing) {
    return false;
  }
  await backupExistingFile(configPath);
  await writeFile(configPath, cleaned, { mode: 0o600 });
  return true;
}

/**
 * The read-back check: what Kimi would run, parsed out of the text this
 * package wrote. An event whose table is missing, or whose command does not
 * name the script, is a hook that never runs, and install must not claim one.
 */
export function kimiBlockEvents(blockText: string): Map<string, string> {
  const events: Map<string, string> = new Map();
  const table = /\[\[hooks\]\]\nevent = ("(?:[^"\\]|\\.)*")\ncommand = ("(?:[^"\\]|\\.)*")\n/g;
  let match: RegExpExecArray | null = table.exec(blockText);
  while (match !== null) {
    try {
      const event: unknown = JSON.parse(match[1] ?? "");
      const command: unknown = JSON.parse(match[2] ?? "");
      if (typeof event === "string" && typeof command === "string") {
        events.set(event, command);
      }
    } catch {
      // An unparseable table is not a registration; leave it out.
    }
    match = table.exec(blockText);
  }
  return events;
}

async function readOrNull(path: string): Promise<string | null> {
  try {
    return await readFile(path, "utf8");
  } catch {
    return null;
  }
}

/** The staged path one hook file resolves to, for the block renderer. */
export function kimiScriptPath(runtimeDirectory: string, scriptFile: string): string {
  return join(runtimeDirectory, "hooks", scriptFile);
}
