/**
 * The WenYuan connection configuration, shared by the Host plugin and the
 * browser card.
 *
 * A client package must not depend on a Host package, and the browser bundle
 * may only require the shell's seeded platform modules — so both halves spell
 * the same field names and defaults from this import-free module.
 * @module @studyzy/dsh-wenyuan-memory/settings
 */

/**
 * Settings namespace of the Host plugin. In `dsh >= 0.2.0` a plugin's settings
 * namespace IS its profile entry id, so this is the id the bundle patch mounts
 * the Host plugin under. A field reaches the Plugins-page card only when the
 * Host schema marks it `volatile()`.
 */
export const WENYUAN_MEMORY_NS = 'dsh-wenyuan-memory'

/**
 * The credential reference the API key lives under. Never part of a settings
 * section: a secret literal must not ride a settings response, so the Host
 * resolves this reference on every request and the card writes it through the
 * credentials domain, which answers only "configured" or "not configured".
 */
export const API_KEY_REF = 'WENYUAN_API_KEY'

/**
 * The memory bank a session uses when the card names none.
 *
 * It is a *sentinel*, not a bank: one fixed bank is shared by every repository
 * the user opens, so memory learned in one project is recalled in another — and
 * a session working in a repo bank never sees what it wrote there. `auto` makes
 * the bank derived per session instead; see {@link resolveBankId}.
 */
export const DEFAULT_BANK_ID = 'auto'

/**
 * Spellings that asked for the derivation before it existed.
 *
 * `default` was this package's sentinel while the bank was one fixed name, and
 * a card that saved it holds that literal in its stored settings. It is read as
 * {@link DEFAULT_BANK_ID} so an install that never touched the field keeps
 * deriving instead of addressing a bank named `default`, which no deployment
 * provisions.
 */
const LEGACY_AUTO_BANK_IDS: readonly string[] = ['default']

/** The prefix every derived bank id carries, so derived banks group by kind. */
export const BANK_PREFIX = 'coding-agent::'

/** The derived repo a session with no working directory falls back to. */
export const FALLBACK_BANK_REPO = 'workspace'

/** Built-in defaults, kept together so a deployment can retarget them once. */
export const DEFAULT_API_URL = 'http://127.0.0.1:18888'
export const DEFAULT_INJECT_CONTEXT = true
export const DEFAULT_RETAIN_ON_STOP = true

/** The editable connection fields, in the order the card renders them. */
export interface WenYuanMemorySettings {
  /** WenYuan service base URL, without the `/v1` prefix. */
  apiUrl?: string
  /** Memory bank this session reads from and writes to. */
  bankId?: string
  /** Whether the session-start and pre-prompt hooks attach recalled memory. */
  injectContext?: boolean
  /** Whether a finished turn is written back to memory when the turn stops. */
  retainOnStop?: boolean
}

/** One connection as the Host resolved it, with the key unwrapped. */
export interface WenYuanConnection {
  /** Base URL with any trailing slash removed. */
  apiUrl: string
  bankId: string
  /** The resolved API key, absent when none is configured. */
  apiKey?: string
}

/**
 * The bank-scoped route prefix every memory operation hangs off, matching the
 * reference's own layout: `<apiUrl>/v1/default/banks/<bankId>`.
 * @param connection - the resolved connection.
 * @param suffix - the operation route, such as `/memories/recall`.
 * @returns the absolute request URL.
 */
export function bankUrl(connection: WenYuanConnection, suffix: string): string {
  return `${connection.apiUrl}/v1/default/banks/${encodeURIComponent(connection.bankId)}${suffix}`
}

/**
 * Normalize one URL: trim, drop trailing slashes, and drop a trailing `/v1`.
 * {@link bankUrl} appends the `/v1/default/banks/...` route itself, so a base
 * that already carries the API prefix would produce `/v1/v1/...` and every
 * call would answer 404 even though the key is valid. Pasting the address out
 * of a client snippet is the common way in, so the prefix is removed instead
 * of trusted. A blank value falls back to the documented default.
 * @param value - the configured value, possibly blank.
 * @returns the usable base URL.
 */
export function normalizeApiUrl(value: string | undefined): string {
  const trimmed = (value ?? '').trim()
  if (trimmed === '') return DEFAULT_API_URL
  return trimmed.replace(/\/+$/, '').replace(/\/v1$/i, '').replace(/\/+$/, '')
}

/**
 * Normalize one bank id: trim, and fall back to the sentinel when blank.
 * @param value - the configured value, possibly blank.
 * @returns the usable bank id, `auto` when nothing usable was configured.
 */
export function normalizeBankId(value: string | undefined): string {
  const trimmed = (value ?? '').trim()
  return trimmed === '' ? DEFAULT_BANK_ID : trimmed
}

/**
 * Whether a configured bank id asks for the per-session derivation.
 * The comparison ignores case, so a card that saved `Auto` is not mistaken for
 * a bank literally named `Auto` while every other value stays verbatim.
 * @param value - a normalized bank id.
 * @returns true when the id is the {@link DEFAULT_BANK_ID} sentinel.
 */
export function isAutoBankId(value: string): boolean {
  const normalized = value.trim().toLowerCase()
  return normalized === DEFAULT_BANK_ID || LEGACY_AUTO_BANK_IDS.includes(normalized)
}

/**
 * Read the repository name out of a git remote URL.
 *
 * Both spellings a remote is configured in are accepted — the scp-like
 * `git@host:owner/repo.git` and the URL-like `https://host/owner/repo(.git)`
 * (also `ssh://` and `git://`) — because which one a clone used is the user's
 * choice, not a fact this plugin can depend on. The last path segment is the
 * name; a trailing `.git` and trailing slashes are dropped.
 * @param remoteUrl - one `remote.origin.url` value, possibly blank.
 * @returns the repository name, or undefined when the value names none.
 */
export function parseGitRepo(remoteUrl: string | undefined): string | undefined {
  const trimmed = (remoteUrl ?? '').trim()
  if (trimmed === '') return undefined
  const path = trimmed
    .replace(/^[a-z][a-z0-9+.-]*:\/\//i, '')
    .replace(/^[^/:@]+@/, '')
    .replace(/^[^/:]+:/, '')
  const segments = path.split('/').filter((segment) => segment !== '')
  const last = segments.at(-1)?.replace(/\.git$/i, '').replace(/[.]*$/, '') ?? ''
  // A remote that ends in a separator, or carries nothing the URL can name,
  // names no repository rather than the empty string.
  return /[A-Za-z0-9._-]/.test(last) ? last : undefined
}

/**
 * The bank id for one repository name.
 * @param repo - a repository or directory name.
 * @returns `coding-agent::<repo>`, or the workspace fallback when the name is
 *   blank or unfit to be a path segment.
 */
export function autoBankId(repo: string | undefined): string {
  const name = (repo ?? '').trim()
  const usable = /^[A-Za-z0-9._-]+$/.test(name) && name !== '.' && name !== '..'
  return `${BANK_PREFIX}${usable ? name : FALLBACK_BANK_REPO}`
}

/**
 * Resolve the bank a session reads from and writes to.
 *
 * An explicit bank id is used verbatim — a deployment that names a bank to share
 * across projects must keep working. Only the `auto` sentinel, a blank, or the
 * legacy `default` spelling derives: the repository name the Host read from the
 * working directory's git remote, then the directory's own name, then
 * {@link FALLBACK_BANK_REPO} when the session has no working directory at all.
 * @param configured - the normalized `bankId` setting.
 * @param scope - what the session's place resolves to: its working directory,
 *   and the repository name read from it when one could be read.
 * @returns the bank id this session's requests address.
 */
export function resolveBankId(
  configured: string | undefined,
  scope: { cwd?: string | undefined; repo?: string | undefined } = {},
): string {
  const value = normalizeBankId(configured)
  if (!isAutoBankId(value)) return value
  if (scope.repo !== undefined && scope.repo.trim() !== '') return autoBankId(scope.repo)
  const cwd = (scope.cwd ?? '').trim()
  if (cwd === '') return autoBankId(undefined)
  const base = cwd.split(/[\\/]+/).filter((segment) => segment !== '').at(-1)
  return autoBankId(base)
}