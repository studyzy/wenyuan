/**
 * The WenYuan HTTP client.
 *
 * Every call goes through the plain runtime `fetch`: the reference's routes are
 * the contract, so there is no vendor SDK in the dependency tree and nothing to
 * keep in step with a client release. The bank-scoped prefix and the request
 * bodies below are exactly the reference's.
 *
 * Fail-open is the rule at every boundary. A memory service that is slow,
 * unreachable, or answering an error must degrade to an ordinary turn — never
 * to a failed one — so every method resolves to an outcome instead of throwing.
 * @module @studyzy/dsh-wenyuan-memory/client
 */

import { bankUrl, type WenYuanConnection } from './settings.ts'

/** The recall request body, matching the reference's route. */
interface RecallRequest {
  query: string
}

/** One item of the retain request body, matching the reference's route. */
export interface RetainItem {
  /** The text to remember. */
  content: string
  /** Who is speaking, so extraction attributes a claim to the right speaker. */
  context?: string
  /** Document identity; a re-retain of the same id replaces rather than duplicates. */
  document_id: string
  /** Provenance and routing tags. */
  tags?: string[]
  /** The extraction strategy the bank was configured with. */
  strategy?: string
  /** ISO timestamp the document is dated at. */
  timestamp?: string
  /** Free-form metadata the document carries. */
  metadata?: Record<string, unknown>
}

/** What one recall resolved to. */
export type RecallOutcome =
  | { readonly ok: true; readonly texts: readonly string[] }
  | { readonly ok: false; readonly reason: string }

/** What one retain resolved to. */
export type RetainOutcome =
  | { readonly ok: true; readonly operationId?: string }
  | { readonly ok: false; readonly reason: string }

/** How one client instance is built. */
export interface MemoryClientOptions {
  /** The resolved connection: base URL, bank, and the API key when configured. */
  readonly connection: WenYuanConnection
  /** Per-request deadline in milliseconds. */
  readonly timeoutMs: number
  /** Injectable for tests; defaults to the runtime `fetch`. */
  readonly fetchImpl?: typeof fetch
}

/** One request body's parsed answer, or the reason it did not land. */
type PostOutcome =
  | { readonly ok: true; readonly body: unknown }
  | { readonly ok: false; readonly reason: string }

/**
 * Pull the recalled texts out of a recall response.
 *
 * The reference answers `{ results: [{ text }] }`; a deployment still on an
 * older shape may answer `{ memories: [...] }`, so both are read and anything
 * unreadable counts as no text rather than as an error.
 * @param body - the parsed response.
 * @returns the non-empty texts, in answer order.
 */
export function textsFromRecall(body: unknown): string[] {
  if (body === null || typeof body !== 'object') return []
  const record = body as Record<string, unknown>
  const results = Array.isArray(record['results'])
    ? record['results']
    : Array.isArray(record['memories'])
      ? record['memories']
      : []
  const texts: string[] = []
  for (const item of results) {
    if (item === null || typeof item !== 'object') continue
    const text = (item as Record<string, unknown>)['text']
    if (typeof text === 'string' && text.trim() !== '') texts.push(text.trim())
  }
  return texts
}

/** One client bound to one resolved connection. */
export class WenYuanClient {
  private readonly base: string
  private readonly token: string | undefined
  private readonly timeoutMs: number
  private readonly fetchImpl: typeof fetch

  /**
   * @param options - the connection, the deadline, and an injectable fetch.
   */
  constructor(options: MemoryClientOptions) {
    this.base = bankUrl(options.connection, '')
    this.token = options.connection.apiKey
    this.timeoutMs = options.timeoutMs
    this.fetchImpl = options.fetchImpl ?? fetch
  }

  /**
   * Recall what the bank already knows about a query.
   * @param query - the recall question.
   * @param signal - the caller's cancellation, when one stands.
   * @returns the recalled texts, or the reason the call did not land.
   */
  async recall(query: string, signal?: AbortSignal): Promise<RecallOutcome> {
    const request: RecallRequest = { query }
    const answer = await this.post('/memories/recall', request, signal)
    if (!answer.ok) return { ok: false, reason: answer.reason }
    return { ok: true, texts: textsFromRecall(answer.body) }
  }

  /**
   * Capture one or more items into memory.
   *
   * The write is asynchronous at the service (`async: true`), so a resolved
   * call means the service accepted the work, not that extraction finished.
   * @param items - the items to retain.
   * @param signal - the caller's cancellation, when one stands.
   * @returns whether the service accepted the write, and its operation id.
   */
  async retain(items: readonly RetainItem[], signal?: AbortSignal): Promise<RetainOutcome> {
    if (items.length === 0) return { ok: true }
    const answer = await this.post('/memories', { items, async: true }, signal)
    if (!answer.ok) return { ok: false, reason: answer.reason }
    const operationId =
      answer.body !== null && typeof answer.body === 'object'
        ? (answer.body as Record<string, unknown>)['operation_id']
        : undefined
    return {
      ok: true,
      ...typeof operationId === 'string' ? { operationId } : {},
    }
  }

  /**
   * One POST, bounded by the deadline and by the caller's cancellation.
   * @param suffix - the bank-scoped route.
   * @param body - the JSON body.
   * @param signal - the caller's cancellation, when one stands.
   * @returns the parsed answer, or the failure reason.
   */
  private async post(suffix: string, body: unknown, signal?: AbortSignal): Promise<PostOutcome> {
    const timeout = new AbortController()
    const timer = setTimeout(() => { timeout.abort() }, this.timeoutMs)
    const combined = signal === undefined ? timeout.signal : AbortSignal.any([timeout.signal, signal])
    const headers: Record<string, string> = { 'content-type': 'application/json' }
    if (this.token !== undefined && this.token !== '') headers['authorization'] = `Bearer ${this.token}`
    try {
      const response = await this.fetchImpl(`${this.base}${suffix}`, {
        method: 'POST',
        headers,
        body: JSON.stringify(body),
        signal: combined,
      })
      if (!response.ok) return { ok: false, reason: `HTTP ${response.status}` }
      // An accepted write may answer with no body at all; that is still a
      // success, so a parse failure reads as an empty document.
      const text = await response.text()
      if (text.trim() === '') return { ok: true, body: undefined }
      try {
        return { ok: true, body: JSON.parse(text) }
      } catch {
        return { ok: true, body: undefined }
      }
    } catch (error) {
      const reason = error instanceof Error ? error.message : String(error)
      return { ok: false, reason }
    } finally {
      clearTimeout(timer)
    }
  }
}