/**
 * Which coding agent wrote a document, resolved to a display entry.
 *
 * The convention is owned by whoever writes the document, not by this UI: a
 * document carries the agent's id both as a `harness` metadata field and as a
 * `harness:<id>` tag so it stays filterable. Metadata wins over the tag, since
 * the tag is a projection that a person can edit in the UI.
 *
 * An unknown id is not an error. It resolves to nothing here, and callers fall
 * back to showing the raw value as an ordinary metadata chip, so a new agent is
 * never invisible just because its mark is not registered yet.
 *
 * The ids below are exactly the ones the coding-agent integration emits — one
 * per hook-driven agent, plus the persistent-plugin agents whose id is the
 * argument their entrypoint passes to the shared factory they build on. Do not
 * register an id nothing writes: a mark nothing renders is dead weight.
 */

export interface HarnessLogoEntry {
  /** Canonical agent id, e.g. "claude-code". */
  id: string;
  /** Display name, a proper noun and deliberately not translated. */
  label: string;
  /** Path under the public directory. Callers run it through the base-path helper. */
  src: string;
  /** Set when the mark is dark-on-transparent and would vanish on a dark page. */
  invertOnDark?: boolean;
}

/** Prefix that marks a tag as carrying the writing agent's id. */
export const HARNESS_TAG_PREFIX = "harness:";

/**
 * Registered agents. Ids match what the writers emit, lowercased and with
 * whitespace collapsed to hyphens; an id nothing writes would be an entry
 * nothing renders.
 */
export const HARNESS_LOGO_REGISTRY: Record<string, HarnessLogoEntry> = {
  "antigravity-cli": {
    id: "antigravity-cli",
    label: "Antigravity CLI",
    src: "/img/harness/antigravity-cli.png",
  },
  "claude-code": { id: "claude-code", label: "Claude Code", src: "/img/harness/claude-code.png" },
  "cline-cli": {
    id: "cline-cli",
    label: "Cline CLI",
    src: "/img/harness/cline-cli.svg",
    invertOnDark: true,
  },
  codex: { id: "codex", label: "Codex", src: "/img/harness/codex.svg", invertOnDark: true },
  dcode: { id: "dcode", label: "DeepAgents Dcode", src: "/img/harness/dcode.svg" },
  "factory-droid": {
    id: "factory-droid",
    label: "Factory Droid",
    src: "/img/harness/factory-droid.svg",
    invertOnDark: true,
  },
  "copilot-cli": {
    id: "copilot-cli",
    label: "GitHub Copilot CLI",
    src: "/img/harness/copilot-cli.svg",
    invertOnDark: true,
  },
  "cursor-cli": {
    id: "cursor-cli",
    label: "Cursor CLI",
    src: "/img/harness/cursor-cli.svg",
    invertOnDark: true,
  },
  "devin-cli": {
    id: "devin-cli",
    label: "Devin CLI",
    src: "/img/harness/devin-cli.svg",
    invertOnDark: true,
  },
  dsh: { id: "dsh", label: "DeepSeek Harness", src: "/img/harness/dsh.svg", invertOnDark: true },
  // Retired: the id the integration used to emit before it moved on. The entry
  // stays so documents retained under it keep their mark.
  gemini: { id: "gemini", label: "Gemini", src: "/img/harness/gemini.svg" },
  // Not inverted: this one is a filled tile with a contrasting glyph, so it
  // stays legible on dark without inverting.
  "grok-build": { id: "grok-build", label: "Grok Build", src: "/img/harness/grok-build.svg" },
  "kimi-code": {
    id: "kimi-code",
    label: "Kimi Code",
    src: "/img/harness/kimi-code.svg",
    invertOnDark: true,
  },
  kilo: { id: "kilo", label: "Kilo CLI", src: "/img/harness/kilo.svg" },
  opencode: { id: "opencode", label: "OpenCode", src: "/img/harness/opencode.png" },
  // The second major line ships as its own binary and reports its own id, but it
  // is the same product, so it shares the first line's mark.
  opencode2: { id: "opencode2", label: "OpenCode 2", src: "/img/harness/opencode.png" },
  pi: { id: "pi", label: "pi", src: "/img/harness/pi.svg", invertOnDark: true },
  "qwen-code": { id: "qwen-code", label: "Qwen Code", src: "/img/harness/qwen-code.svg" },
  "prime-agent": {
    id: "prime-agent",
    label: "Prime Agent",
    src: "/img/harness/prime-agent.svg",
    invertOnDark: true,
  },
  zcode: { id: "zcode", label: "ZCode", src: "/img/harness/zcode.svg" },
  traecode: { id: "traecode", label: "TraeCode", src: "/img/harness/traecode.png" },
  workbuddy: { id: "workbuddy", label: "WorkBuddy", src: "/img/harness/workbuddy.png" },
  codebuddy: { id: "codebuddy", label: "CodeBuddy", src: "/img/harness/codebuddy.png" },
};

/** Resolve an agent id to its registry entry, or null when unrecognised. */
export function resolveHarnessLogo(value: string | null | undefined): HarnessLogoEntry | null {
  if (!value) return null;
  const key = value.trim().toLowerCase().replace(/[\s_]+/g, "-");
  return HARNESS_LOGO_REGISTRY[key] ?? null;
}

/**
 * Read the writing agent's id from a document's metadata, falling back to its
 * `harness:<id>` tag. Returns the raw value; resolving it to a display entry is
 * a separate step so an unknown agent can still be reported as text.
 */
export function documentHarness(
  metadata: Record<string, unknown> | null | undefined,
  tags: readonly string[] | null | undefined,
): string | null {
  const fromMetadata = metadata?.harness;
  if (typeof fromMetadata === "string" && fromMetadata.trim()) {
    return fromMetadata.trim();
  }
  for (const tag of tags ?? []) {
    if (tag.toLowerCase().startsWith(HARNESS_TAG_PREFIX)) {
      const value = tag.slice(HARNESS_TAG_PREFIX.length).trim();
      if (value) return value;
    }
  }
  return null;
}