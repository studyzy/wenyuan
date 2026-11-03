/**
 * Carry the PRE-PORT install over.
 *
 * Two artifacts of the pre-port package survive on a machine that was wired
 * before the shared-lifecycle port, and both are detected here so the session
 * can say so and the wave-4 uninstall/migrate machinery can act:
 *
 *  1. The generated hook scripts the pre-port installer staged under the
 *     per-user runtime (~/.wenyuan/wenyuan-memory/hooks/*.mjs) plus the host
 *     config registrations that still launch them. Running both generations is
 *     not harmless: each recalls into every prompt and retains every
 *     transcript, so the agent sees two memory blocks and conversations land
 *     twice. The installer refreshes a registration it owns, so the session
 *     start is where a STILL-STALE wiring is named.
 *  2. The stored connection the pre-port CLI wrote (~/.wenyuan/config.json).
 *     Where memory lives, and the credential to reach it, are decisions an
 *     operator already made; adopting them is the conservative move, not the
 *     convenient one.
 */
import { existsSync, readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { DEFAULT_API_PORT } from "../config/connection";
import { STORED_CONFIG_FILE_NAME, WENYUAN_DIRECTORY_NAME } from "../config/paths";
import { HOOK_SCRIPT_FILES } from "../hosts/lifecycle";
import { HOOKS_SUBDIRECTORY } from "../runtime/layout";
import { OWNERSHIP_MARKER } from "../write/marker";

/** The runtime directory's hooks segment, as every pre-port command path spells it. */
const LEGACY_HOOKS_SEGMENT = `${OWNERSHIP_MARKER}/${HOOKS_SUBDIRECTORY}/`;

/** The generated script files the pre-port installer staged, for the marker check. */
const LEGACY_HOOK_SCRIPT_NAMES: readonly string[] = Object.values(HOOK_SCRIPT_FILES);

/**
 * The staged runtime directory of a pre-port hook install when it is still
 * registered for Claude Code, else undefined.
 *
 * Read from Claude's own settings file and matched by SHAPE, not by name: a
 * hook command this package recognizes is one whose command string carries the
 * ownership marker and points at one of the generated hook scripts under the
 * runtime's hooks directory. Any read failure means "not detected" — this only
 * ever adds a warning. The `cwd` argument stays for seam parity with the
 * session-start caller: settings registrations are user-scope, so unlike the
 * reference's project-scoped plugin registry they need no per-project check.
 */
export function detectLegacyClaudePlugin(
  _cwd: string,
  claudeDir: string = join(homedir(), ".claude")
): string | undefined {
  const settings = readJson(join(claudeDir, "settings.json"));
  if (!settings) return undefined;
  const hit = findLegacyHookPath(settings);
  if (hit === undefined) return undefined;
  // The runtime directory is the actionable name: uninstall and re-install
  // both operate on it, and the warning renders it. The command wraps the
  // script path in quotes (`node "<path>"`), so the directory starts right
  // after the opening quote — or at the string start for a bare path.
  const cut = hit.indexOf(LEGACY_HOOKS_SEGMENT);
  if (cut < 0) return undefined;
  const start = hit.lastIndexOf('"', cut) + 1;
  return hit.slice(start, cut + OWNERSHIP_MARKER.length);
}

/** Depth-first search for a hook command string naming a staged pre-port script. */
function findLegacyHookPath(value: unknown): string | undefined {
  if (typeof value === "string") {
    if (value.includes(LEGACY_HOOKS_SEGMENT)) {
      for (const script of LEGACY_HOOK_SCRIPT_NAMES) {
        if (value.includes(script)) return value;
      }
    }
    return undefined;
  }
  if (Array.isArray(value)) {
    for (const item of value) {
      const found = findLegacyHookPath(item);
      if (found !== undefined) return found;
    }
    return undefined;
  }
  if (value !== null && typeof value === "object") {
    for (const member of Object.values(value)) {
      const found = findLegacyHookPath(member);
      if (found !== undefined) return found;
    }
  }
  return undefined;
}

function readJson(path: string): Record<string, unknown> | undefined {
  try {
    const parsed: unknown = JSON.parse(readFileSync(path, "utf8"));
    return parsed && typeof parsed === "object" ? (parsed as Record<string, unknown>) : undefined;
  } catch {
    return undefined;
  }
}

/** The user-visible SessionStart warning for a still-registered pre-port hook install. */
export function legacyClaudePluginWarning(runtimeDir: string): string {
  return (
    `⚠️ The pre-port WenYuan hooks are still registered (${runtimeDir}) — they run alongside this ` +
    `install, so memory is recalled and retained twice.\n` +
    `  ↳ refresh the wiring: wenyuan-agents install (or wenyuan-agents uninstall to remove it)`
  );
}

/**
 * The stored connection file the pre-port CLI wrote, per host.
 *
 * The pre-port package kept ONE shared per-user file rather than one per
 * agent, so every entry names the same file — the map shape stays, because the
 * session-start seam and the installer both look a host up by id.
 */
export const LEGACY_CONFIG_FILES: Record<string, string> = {
  "claude-code": STORED_CONFIG_FILE_NAME,
  codex: STORED_CONFIG_FILE_NAME,
  codebuddy: STORED_CONFIG_FILE_NAME,
  workbuddy: STORED_CONFIG_FILE_NAME,
  "cursor-cli": STORED_CONFIG_FILE_NAME,
  "copilot-cli": STORED_CONFIG_FILE_NAME,
};

/** Where the pre-port CLI told users to put their connection settings. */
export function legacyConfigPath(harness: string, home: string = homedir()): string {
  return join(home, WENYUAN_DIRECTORY_NAME, LEGACY_CONFIG_FILES[harness] ?? STORED_CONFIG_FILE_NAME);
}

export interface LegacyEndpoint {
  /** Which host lookup found it, for the installer's output. */
  harness: string;
  serverMode: "hosted" | "self-hosted" | "local";
  apiUrl?: string;
  apiToken?: string;
  /** Only when it differs from the default — in local mode the port IS the endpoint. */
  apiPort?: number;
  /** The file it came from, so the installer can say where it looked. */
  source: string;
}

const SERVER_MODES: readonly string[] = ["hosted", "self-hosted", "local"];

/**
 * Read the stored pre-port connection, or undefined when it was never written.
 *
 * An ABSENT `apiUrl` is meaningful rather than missing: the pre-port flow
 * treated an unset URL as "use the local service on `apiPort`", so a config
 * that never set one describes a local deployment, not an unconfigured user.
 */
export function readLegacyEndpoint(
  home: string = homedir(),
  prefer: readonly string[] = []
): LegacyEndpoint | undefined {
  // Look at the hosts being installed FIRST; falls through to any other known
  // entry. The file is shared today, so the order rarely matters — but the
  // shape lets a future per-host split land without touching the callers.
  const order = [...prefer, ...Object.keys(LEGACY_CONFIG_FILES)].filter(
    (h, i, all) => h in LEGACY_CONFIG_FILES && all.indexOf(h) === i
  );
  for (const harness of order) {
    const found = readOne(harness, home);
    if (found) return found;
  }
  return undefined;
}

function readOne(harness: string, home: string): LegacyEndpoint | undefined {
  const source = legacyConfigPath(harness, home);
  if (!existsSync(source)) return undefined;
  let raw: Record<string, unknown>;
  try {
    raw = JSON.parse(readFileSync(source, "utf8")) as Record<string, unknown>;
  } catch {
    return undefined; // a config we cannot read is not a decision we can honour
  }
  if (!raw || typeof raw !== "object") return undefined;

  const apiToken = typeof raw.apiToken === "string" ? raw.apiToken : undefined;
  const url = typeof raw.apiUrl === "string" ? raw.apiUrl.trim() : "";
  const storedMode =
    typeof raw.serverMode === "string" && SERVER_MODES.includes(raw.serverMode)
      ? (raw.serverMode as LegacyEndpoint["serverMode"])
      : undefined;

  if (url) {
    return {
      harness,
      serverMode: storedMode ?? "self-hosted",
      apiUrl: url,
      ...(apiToken ? { apiToken } : {}),
      source,
    };
  }
  const port = typeof raw.apiPort === "number" ? raw.apiPort : undefined;
  return {
    harness,
    serverMode: storedMode ?? "local",
    ...(apiToken ? { apiToken } : {}),
    ...(port && port !== DEFAULT_API_PORT ? { apiPort: port } : {}),
    source,
  };
}
