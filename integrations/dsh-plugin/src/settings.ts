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

/** Built-in defaults, kept together so a deployment can retarget them once. */
export const DEFAULT_API_URL = 'http://127.0.0.1:18888'
export const DEFAULT_BANK_ID = 'default'
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
 * Normalize one bank id: trim, and fall back to the default when blank.
 * @param value - the configured value, possibly blank.
 * @returns the usable bank id.
 */
export function normalizeBankId(value: string | undefined): string {
  const trimmed = (value ?? '').trim()
  return trimmed === '' ? DEFAULT_BANK_ID : trimmed
}