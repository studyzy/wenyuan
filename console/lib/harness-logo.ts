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
 * The registry ships no third-party brand artwork. Entries carry a label and an
 * optional local asset path; a registered id with no `src` renders as a label
 * chip rather than an image.
 */

export interface HarnessLogoEntry {
  /** Canonical agent id, e.g. "claude-code". */
  id: string;
  /** Display name, a proper noun and deliberately not translated. */
  label: string;
  /** Path under the public directory. Callers run it through the base-path helper. */
  src?: string;
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
  "claude-code": { id: "claude-code", label: "Claude Code" },
  codex: { id: "codex", label: "Codex" },
  "gemini-cli": { id: "gemini-cli", label: "Gemini CLI" },
  "qwen-code": { id: "qwen-code", label: "Qwen Code" },
  opencode: { id: "opencode", label: "opencode" },
  cursor: { id: "cursor", label: "Cursor" },
  copilot: { id: "copilot", label: "GitHub Copilot" },
};

function normalizeHarnessId(value: string): string {
  return value.trim().toLowerCase().replace(/[\s_]+/g, "-");
}

/** Resolve an agent id to its registry entry, or null when unrecognised. */
export function resolveHarnessLogo(value: string | null | undefined): HarnessLogoEntry | null {
  if (!value) return null;
  return HARNESS_LOGO_REGISTRY[normalizeHarnessId(value)] ?? null;
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