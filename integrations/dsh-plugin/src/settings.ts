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
 * The tenant prefix a bank id carries when the card names no tenant.
 *
 * A bank id is `<tenant>::<project>`: the tenant is the user name configured on
 * the card, the project is derived per session. A card that names no tenant
 * keeps the historical `coding-agent` prefix, so an existing install addresses
 * the same banks it did before the field became a tenant name.
 */
export const DEFAULT_TENANT_NAME = 'coding-agent'

/**
 * Spellings the card may still hold from the bank-era field.
 *
 * `auto` and `default` named the derivation when this field was a bank id; a
 * stored value of either one reads as "no tenant configured" instead of a
 * tenant literally called `auto`.
 */
const UNCONFIGURED_TENANTS: readonly string[] = ['auto', 'default']

/** The project a session with no working directory falls back to. */
export const FALLBACK_PROJECT = 'workspace'

/** Built-in defaults, kept together so a deployment can retarget them once. */
export const DEFAULT_API_URL = 'http://127.0.0.1:18888'
export const DEFAULT_INJECT_CONTEXT = true
export const DEFAULT_RETAIN_ON_STOP = true

/** The editable connection fields, in the order the card renders them. */
export interface WenYuanMemorySettings {
  /** WenYuan service base URL, without the `/v1` prefix. */
  apiUrl?: string
  /** Tenant (user) name; the bank id is `<tenant>::<project>`. */
  tenantName?: string
  /** Whether the session-start and pre-prompt hooks attach recalled memory. */
  injectContext?: boolean
  /** Whether a finished turn is written back to memory when the turn stops. */
  retainOnStop?: boolean
}

/** One connection as the Host resolved it, with the key unwrapped. */
export interface WenYuanConnection {
  /** Base URL with any trailing slash removed. */
  apiUrl: string
  /** The resolved bank id: `<tenant>::<project>`. */
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
 * Normalize one tenant name: trim, and fall back to the default tenant when
 * blank. The legacy `auto` / `default` spellings are treated the same way, so
 * a stored value from the bank-era field never becomes a tenant name.
 * @param value - the configured value, possibly blank.
 * @returns the usable tenant name.
 */
export function normalizeTenantName(value: string | undefined): string {
  const trimmed = (value ?? '').trim()
  if (trimmed === '' || UNCONFIGURED_TENANTS.includes(trimmed.toLowerCase())) {
    return DEFAULT_TENANT_NAME
  }
  return trimmed
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
 * The project segment of a bank id, for one repository or directory name.
 * @param name - a repository or directory name.
 * @returns the name, or the workspace fallback when it is blank or unfit to be
 *   a path segment.
 */
function bankProject(name: string | undefined): string {
  const trimmed = (name ?? '').trim()
  const usable = /^[A-Za-z0-9._-]+$/.test(trimmed) && trimmed !== '.' && trimmed !== '..'
  return usable ? trimmed : FALLBACK_PROJECT
}

/**
 * The bank id a session uses when the card names no tenant.
 * @param repo - a repository or directory name.
 * @returns `coding-agent::<repo>`, or the workspace fallback when the name is
 *   blank or unfit to be a path segment.
 */
export function autoBankId(repo: string | undefined): string {
  return `${DEFAULT_TENANT_NAME}::${bankProject(repo)}`
}

/**
 * Resolve the bank a session reads from and writes to.
 *
 * The card configures the tenant (user) name, never a bank: the bank is
 * `<tenant>::<project>`, where the project comes from the repository name the
 * Host read from the working directory's git remote, then the directory's own
 * name, then {@link FALLBACK_PROJECT} when the session has no working directory
 * at all. A blank tenant — or the legacy `auto` / `default` spellings — uses
 * {@link DEFAULT_TENANT_NAME}, which is the `coding-agent::<project>` ids this
 * package produced before the field became a tenant name.
 * @param tenantName - the configured tenant name.
 * @param scope - what the session's place resolves to: its working directory,
 *   and the repository name read from it when one could be read.
 * @returns the bank id this session's requests address.
 */
export function resolveBankId(
  tenantName: string | undefined,
  scope: { cwd?: string | undefined; repo?: string | undefined } = {},
): string {
  const tenant = normalizeTenantName(tenantName)
  const repo = (scope.repo ?? '').trim()
  if (repo !== '') return `${tenant}::${bankProject(repo)}`
  const cwd = (scope.cwd ?? '').trim()
  const base = cwd.split(/[\\/]+/).filter((segment) => segment !== '').at(-1)
  return `${tenant}::${bankProject(base)}`
}