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

/** What one knowledge-base read resolved to. */
export type KnowledgeOutcome<T> =
  | { readonly ok: true; readonly value: T }
  | { readonly ok: false; readonly reason: string }

/** One page as the knowledge tree lists it. */
export interface KnowledgeTreeNode {
  readonly id: string
  readonly kind?: string
  readonly name: string
  readonly description?: string
  readonly folder?: string
  readonly parent_id?: string | null
  readonly mental_model_id?: string
  readonly source_query?: string
}

/** One page as the tree returns it, including its children. */
export interface KnowledgeTreeFolder extends KnowledgeTreeNode {
  readonly children?: readonly KnowledgeTreeNode[]
}

/** The tree answer. */
export interface KnowledgeTree {
  readonly roots: readonly KnowledgeTreeNode[]
}

/** One search hit: the page plus the snippet that matched. */
export interface KnowledgeHit {
  readonly id: string
  readonly name: string
  readonly description?: string
  readonly snippet?: string
  readonly source_query?: string
}

/** The search answer. */
export interface KnowledgeSearch {
  readonly results: readonly KnowledgeHit[]
}

/** One page read in full. */
export interface KnowledgePage {
  readonly id: string
  readonly name: string
  readonly description?: string
  readonly type?: string
  readonly tags?: readonly string[]
  readonly timestamp?: string
  readonly body?: string
  readonly markdown?: string
  readonly last_updated_at?: string
}

/** What one client instance is built for this bank. */
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
   * Search the bank's knowledge pages: curated documents synthesized from
   * memories, not the raw memories themselves.
   * @param query - what to search for.
   * @param limit - the result cap the service accepts (1-50).
   * @param signal - the caller's cancellation, when one stands.
   * @returns the ranked hits, or the reason the call did not land.
   */
  async searchKnowledge(query: string, limit: number, signal?: AbortSignal): Promise<KnowledgeOutcome<KnowledgeSearch>> {
    const params = new URLSearchParams({ q: query, limit: String(limit) })
    const answer = await this.request('GET', `/knowledge-base/search?${params.toString()}`, undefined, signal)
    if (!answer.ok) return { ok: false, reason: answer.reason }
    return { ok: true, value: asSearch(answer.body) }
  }

  /**
   * List the bank's knowledge pages as a tree of folders and pages.
   * @param signal - the caller's cancellation, when one stands.
   * @returns the tree, or the reason the call did not land.
   */
  async listKnowledgePages(signal?: AbortSignal): Promise<KnowledgeOutcome<KnowledgeTree>> {
    const answer = await this.request('GET', '/knowledge-base/tree', undefined, signal)
    if (!answer.ok) return { ok: false, reason: answer.reason }
    return { ok: true, value: asTree(answer.body) }
  }

  /**
   * Read one knowledge page in full.
   * @param pageId - the page's node id.
   * @param signal - the caller's cancellation, when one stands.
   * @returns the page, or the reason the call did not land.
   */
  async readKnowledgePage(pageId: string, signal?: AbortSignal): Promise<KnowledgeOutcome<KnowledgePage>> {
    const answer = await this.request('GET', `/knowledge-base/pages/${encodeURIComponent(pageId)}`, undefined, signal)
    if (!answer.ok) return { ok: false, reason: answer.reason }
    return { ok: true, value: asPage(answer.body) }
  }

  /**
   * One request, bounded by the deadline and by the caller's cancellation.
   * @param method - the HTTP method.
   * @param suffix - the bank-scoped route, query already encoded.
   * @param body - the JSON body for a write, undefined for a read.
   * @param signal - the caller's cancellation, when one stands.
   * @returns the parsed answer, or the failure reason.
   */
  private async request(
    method: 'GET' | 'POST',
    suffix: string,
    body: unknown,
    signal?: AbortSignal,
  ): Promise<PostOutcome> {
    const timeout = new AbortController()
    const timer = setTimeout(() => { timeout.abort() }, this.timeoutMs)
    const combined = signal === undefined ? timeout.signal : AbortSignal.any([timeout.signal, signal])
    const headers: Record<string, string> = {}
    if (body !== undefined) headers['content-type'] = 'application/json'
    if (this.token !== undefined && this.token !== '') headers['authorization'] = `Bearer ${this.token}`
    try {
      const response = await this.fetchImpl(`${this.base}${suffix}`, {
        method,
        headers,
        ...body === undefined ? {} : { body: JSON.stringify(body) },
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

  /**
   * One POST, bounded by the deadline and by the caller's cancellation.
   * @param suffix - the bank-scoped route.
   * @param body - the JSON body.
   * @param signal - the caller's cancellation, when one stands.
   * @returns the parsed answer, or the failure reason.
   */
  private post(suffix: string, body: unknown, signal?: AbortSignal): Promise<PostOutcome> {
    return this.request('POST', suffix, body, signal)
  }
}

/** Read one string member, or undefined when it is absent or not a string. */
function memberText(value: unknown, key: string): string | undefined {
  if (value === null || typeof value !== 'object') return undefined
  const found = Reflect.get(value, key)
  return typeof found === 'string' ? found : undefined
}

/** Read one array member, or an empty list when it is absent or malformed. */
function memberList(value: unknown, key: string): readonly unknown[] {
  if (value === null || typeof value !== 'object') return []
  const found = Reflect.get(value, key)
  return Array.isArray(found) ? found : []
}

/** Project one node the tree or a search answer carried. */
function asNode(value: unknown): KnowledgeTreeNode {
  const id = memberText(value, 'id') ?? ''
  const children = memberList(value, 'children').map(asNode)
  return {
    id,
    name: memberText(value, 'name') ?? id,
    ...pick(memberText(value, 'kind'), 'kind'),
    ...pick(memberText(value, 'description'), 'description'),
    ...pick(memberText(value, 'parent_id'), 'parent_id'),
    ...pick(memberText(value, 'mental_model_id'), 'mental_model_id'),
    ...pick(memberText(value, 'source_query'), 'source_query'),
    ...children.length === 0 ? {} : { children },
  } as KnowledgeTreeNode
}

/** Spread one optional member, so an absent key stays absent. */
function pick<T>(value: T | undefined, key: string): Record<string, T> {
  return value === undefined ? {} : { [key]: value }
}

/** Project the tree answer. */
function asTree(body: unknown): KnowledgeTree {
  return { roots: memberList(body, 'roots').map(asNode) }
}

/** Project the search answer. */
function asSearch(body: unknown): KnowledgeSearch {
  return {
    results: memberList(body, 'results').map((hit) => ({
      id: memberText(hit, 'id') ?? '',
      name: memberText(hit, 'name') ?? '',
      ...pick(memberText(hit, 'description'), 'description'),
      ...pick(memberText(hit, 'snippet'), 'snippet'),
      ...pick(memberText(hit, 'source_query'), 'source_query'),
    })),
  }
}

/** Project one page read in full. */
function asPage(body: unknown): KnowledgePage {
  const tags = memberList(body, 'tags').filter((tag): tag is string => typeof tag === 'string')
  return {
    id: memberText(body, 'id') ?? '',
    name: memberText(body, 'name') ?? '',
    ...pick(memberText(body, 'description'), 'description'),
    ...pick(memberText(body, 'type'), 'type'),
    ...pick(memberText(body, 'timestamp'), 'timestamp'),
    ...pick(memberText(body, 'body'), 'body'),
    ...pick(memberText(body, 'markdown'), 'markdown'),
    ...pick(memberText(body, 'last_updated_at'), 'last_updated_at'),
    ...tags.length === 0 ? {} : { tags },
  }
}