/**
 * The Host plugin: WenYuan agent memory for a DeepSeek Harness session.
 *
 * Three things, and nothing else:
 *
 * 1. **Context in.** When an agent is created, the bank's knowledge pages are
 *    attached as context, so the session starts knowing what this repository
 *    already established. Recalling per repository (and the conflict rules
 *    around it) are the reference's `autoInject: pages` behaviour.
 * 2. **A prompt-time recall.** The first prompt of a turn that carries new
 *    user text is recalled and attached as the step's context. Each turn
 *    recalls at most once, so a tool loop does not pay for a recall per step.
 * 3. **A write-back.** When a turn stops, everything not yet written is
 *    assembled into the reference's own transcript document — user and
 *    assistant text, one action line per tool call, injected memory stripped —
 *    and retained under one ref-id. The projection's `lastRetainedSeq` marks
 *    what was carried, so a later turn appends only what is new.
 *
 * Two tools are registered globally: `wenyuan_recall` and `wenyuan_retain`.
 *
 * Everything here is fail-open: an unreachable or slow service costs the
 * session its memory, never its run.
 * @module @studyzy/dsh-wenyuan-memory
 */

import type { Context } from '@deepseek-ai/cordis'
import z from '@deepseek-ai/schemastery'
import type { Agent } from '@deepseek-ai/dsh-agent'
import type { PreStepDecision } from '@deepseek-ai/dsh-agent'
import { createUserMessage, type UserMessage } from '@deepseek-ai/dsh-llm'
import type { Session } from '@deepseek-ai/dsh-session'
import type { ToolDefinition, ToolRunContext } from '@deepseek-ai/dsh-tools'
import type {} from '@deepseek-ai/dsh-session-projection'
import {
  WenYuanClient,
  type KnowledgeTreeFolder,
  type KnowledgeTreeNode,
} from './client.ts'
import { contextBlock, RECALL_BLOCK_TAG } from './context-blocks.ts'
import type { WenYuanMemorySource } from './source.ts'
import {
  DEFAULT_API_URL,
  DEFAULT_BANK_ID,
  DEFAULT_INJECT_CONTEXT,
  DEFAULT_RETAIN_ON_STOP,
  normalizeApiUrl,
  normalizeBankId,
  type WenYuanMemorySettings,
} from './settings.ts'
import { renderSessionDocument, type TranscriptTurn } from './transcript.ts'
import { wenYuanTranscriptProjection } from './transcript-projection.ts'

export type * from './settings.ts'
export type { TranscriptTurn } from './transcript.ts'
export type { WenYuanTranscriptState } from './transcript-projection.ts'
export { stripInjectedBlocks } from './context-blocks.ts'
export { textsFromRecall, WenYuanClient } from './client.ts'

/** Cordis plugin identity: the profile entry id the bundle patch mounts. */
export const name = 'dsh-wenyuan-memory'

/** Services this plugin needs before it activates. */
export const inject = ['tools', 'sessionProjections', 'credentials']

/** Producer source stamped on every context this plugin attaches. */
const CONTEXT_SOURCE: WenYuanMemorySource = { kind: 'wenyuan-memory', form: 'recall' }

/** The context the reference sends with every session write-back. */
const RETAIN_CONTEXT =
  'conversation between the user and you (the coding agent): user turns are the user\'s words and decisions, ' +
  'assistant turns are yours. When an assistant turn says something is fixed, passes or works, record it as ' +
  'the agent\'s claim ("the agent reported X fixed"), not as an established fact.'

/** The per-request deadline for a hook's recall: the user is waiting on it. */
const RECALL_TIMEOUT_MS = 10_000

/** The session-start knowledge query, kept broad on purpose. */
const SESSION_START_QUERY = 'What should I know about this project and the user at the start of a session?'

/**
 * Loader schema: the four user-editable settings the Plugins-page card edits.
 *
 * Each field carries a `default()`, never `required()`. The host's settings
 * provider writes one field at a time — `mergeLayers(whole, edits)` layers the
 * edited field over what it read back — so a write can reach the loader schema
 * without the other three. A `required()` field makes that write fail at
 * `fiber.resolveConfig` with `$.<field> missing required value`, which the
 * Plugins page can only report as "the server rejected the value". A default is
 * folded in exactly when the key is absent, which is what keeps a partial write
 * valid; the values match `DEFAULT_*` in `settings.ts`, so a deployment that
 * strips a field still behaves as documented.
 */
export const Config = z.object({
  apiUrl: z.string().default(DEFAULT_API_URL).volatile(),
  bankId: z.string().default(DEFAULT_BANK_ID).volatile(),
  injectContext: z.boolean().default(true).volatile(),
  retainOnStop: z.boolean().default(true).volatile(),
}) as unknown as z<WenYuanMemorySettings>

/** What the Host reads at runtime: the plain values, never the volatile wrappers. */
interface ResolvedSettings {
  apiUrl: string
  bankId: string
  injectContext: boolean
  retainOnStop: boolean
}

/**
 * Unwrap a `volatile()` field.
 *
 * `schemastery >= 3.18.3` hands a volatile field to the plugin as a live
 * `{ get(), set() }` reference, not as the value itself — reading it as a plain
 * value yields `[object Object]` and fails the shape this module expects.
 * @param value - the configured field.
 * @returns the plain value behind it.
 */
function unwrap<T>(value: T | { get(): T } | undefined): T | undefined {
  if (value !== null && typeof value === 'object' && typeof (value as { get?: unknown }).get === 'function') {
    return (value as { get(): T }).get()
  }
  return value as T | undefined
}

/**
 * Resolve the four settings into plain values.
 * @param config - the loader config, possibly carrying volatile wrappers.
 * @returns the usable settings.
 */
export function resolveSettings(config: WenYuanMemorySettings): ResolvedSettings {
  return {
    apiUrl: normalizeApiUrl(unwrap<string>(config.apiUrl)),
    bankId: normalizeBankId(unwrap<string>(config.bankId)),
    injectContext: unwrap<boolean>(config.injectContext) ?? DEFAULT_INJECT_CONTEXT,
    retainOnStop: unwrap<boolean>(config.retainOnStop) ?? DEFAULT_RETAIN_ON_STOP,
  }
}

/**
 * The candidate reference names the API key may be stored under, most
 * specific first.
 *
 * The package's own reference comes first so a user who types a key on the
 * card is the one that wins; a deployment that already exports
 * `WENYUAN_API_TOKEN` behind the service keeps working without anyone touching
 * the card. The resolution below skips a reference out of the credentials
 * grammar instead of throwing, so a name the provider cannot address reads as
 * "not set".
 */
const API_KEY_REFS: readonly string[] = ['WENYUAN_API_KEY', 'WENYUAN_API_TOKEN']

/** The reference the card writes, and the one it reports on. */
export const API_KEY_CARD_REF = 'WENYUAN_API_KEY'

/**
 * Mount the plugin.
 * @param ctx - context exposing the tool registry, the projection registry, and credentials.
 * @param config - the four editable connection settings.
 */
export function apply(ctx: Context, config: WenYuanMemorySettings): void {
  const resolved = resolveSettings(config)

  // The transcript fold is registered per profile, not per session: one
  // awaited registration serves every session the registry drives.
  // `register` is awaited before publishing consumers; the returned handle is a
  // live reader for every session the registry drives.
  const transcriptReady: Promise<{ stateOf(session: Session): readonly TranscriptTurn[] }> =
    ctx.sessionProjections.register(wenYuanTranscriptProjection) as unknown as Promise<{ stateOf(session: Session): readonly TranscriptTurn[] }>

  /**
   * The connection in force right now. The settings are volatile, so a save
   * on the card must reach the next request without a plugin reload; the key
   * is resolved per call because a rotated credential must too.
   * @param signal - cancellation for the credential read.
   * @returns the client, or null when memory should not run.
   */
  const clientNow = async (signal?: AbortSignal): Promise<WenYuanClient | null> => {
    const apiKey = await resolveApiKey(ctx, signal)
    return new WenYuanClient({
      connection: { apiUrl: resolved.apiUrl, bankId: resolved.bankId, ...apiKey === undefined ? {} : { apiKey } },
      timeoutMs: RECALL_TIMEOUT_MS,
    })
  }

  /**
   * Per-session state: which turn was recalled, how much of the folded
   * transcript the last accepted write-back already covered, and the write-back
   * currently in flight.
   */
  interface SessionState {
    recalledTurn: number
    retainedTurns: number
    writing: Promise<void> | undefined
  }
  const state = new WeakMap<Agent, SessionState>()
  const stateOf = (agent: Agent): SessionState => {
    const existing = state.get(agent)
    if (existing !== undefined) return existing
    const fresh: SessionState = { recalledTurn: -1, retainedTurns: 0, writing: undefined }
    state.set(agent, fresh)
    return fresh
  }

  // --- Session start: the bank's knowledge, attached before the first turn. ---
  ctx.on('agent/created', async ({ agent, signal }) => {
    if (!resolved.injectContext || !isTopLevel(agent)) return
    const client = await clientNow(signal)
    if (client === null) return
    const outcome = await client.recall(SESSION_START_QUERY, signal).catch(() => null)
    if (outcome === null || !outcome.ok || outcome.texts.length === 0) return
    agent.inject(memoryMessage(contextBlock(RECALL_BLOCK_TAG, outcome.texts.map((text) => `- ${text}`).join('\n'))))
  })

  // --- Before a prompt: one recall per turn that carries new user text. ---
  ctx.on('agent/pre-step', async ({ agent, messages, turn, signal }, next): Promise<PreStepDecision> => {
    if (!resolved.injectContext || messages.length === 0) return next()
    const perSession = stateOf(agent)
    if (perSession.recalledTurn === turn) return next()
    perSession.recalledTurn = turn
    const query = promptTextOf(messages)
    if (query === '') return next()
    const client = await clientNow(signal)
    if (client === null) return next()
    const outcome = await client.recall(query, signal).catch(() => null)
    // Delegate before folding ours in: a later listener may still reject or
    // rewrite the step, and only an enter decision takes extra context.
    const downstream = await next()
    if (outcome === null || !outcome.ok || outcome.texts.length === 0 || downstream.kind !== 'enter') return downstream
    return {
      ...downstream,
      messages: [...downstream.messages, memoryMessage(contextBlock(RECALL_BLOCK_TAG, outcome.texts.map((text) => `- ${text}`).join('\n')))],
    }
  })

  // --- Turn stop: write back what this session has not handed over yet. ---
  ctx.on('agent/turn-stopping', async ({ agent, signal }): Promise<void> => {
    if (!resolved.retainOnStop || !isTopLevel(agent)) return
    const perSession = stateOf(agent)
    // A previous write-back still in flight is superseded, not raced: the
    // newer one sees every turn the older one saw plus more.
    if (perSession.writing !== undefined) await perSession.writing.catch(() => {})
    const write = writeBack(agent, transcriptReady, clientNow, perSession, signal)
    perSession.writing = write
    await write.catch(() => {})
  })

  // --- The knowledge tools. ---
  for (const tool of knowledgeTools(clientNow)) {
    ctx.effect(() => ctx.tools.register(tool), `dsh-wenyuan-memory: ${tool.name} tool`)
  }
}

/**
 * Read the API key from the credentials domain, trying each candidate
 * reference in turn.
 * @param ctx - a context exposing `credentials`.
 * @param signal - cancellation for the read.
 * @returns the resolved key, or undefined when none is configured.
 */
async function resolveApiKey(ctx: Context, signal?: AbortSignal): Promise<string | undefined> {
  const credentials = ctx.get('credentials')
  if (credentials === undefined) return undefined
  for (const reference of API_KEY_REFS) {
    const ref = reference as unknown as Parameters<typeof credentials.resolve>[0]
    if (signal?.aborted === true) return undefined
    const resolved = await credentials.resolve(ref).catch(() => undefined)
    if (resolved !== undefined && resolved.value !== '') return resolved.value
  }
  return undefined
}

/** The text of the messages a step would admit, joined. */
function promptTextOf(messages: readonly UserMessage[]): string {
  const texts: string[] = []
  for (const message of messages) {
    for (const block of message.content) {
      if (block.type === 'text') texts.push(block.text)
    }
  }
  return texts.join('\n').trim()
}

/** Wrap one context block as an identified user message no later reader mistakes for a prompt. */
function memoryMessage(text: string): UserMessage {
  return createUserMessage({ content: [{ type: 'text', text }], source: CONTEXT_SOURCE })
}

/**
 * Whether an agent is a top-level one. A subagent's session is marked
 * `origin: 'subagent'` in its durable header; such a session shares the parent
 * turn's stop, so injecting there would fight the parent's recall and write the
 * parent's transcript back a second time.
 * @param agent - the agent to classify.
 * @returns true when the agent is a session's own root.
 */
function isTopLevel(agent: Agent): boolean {
  const header = (agent as unknown as { session?: Session }).session?.header
  return header?.origin !== 'subagent'
}

/**
 * Assemble and submit one write-back.
 * @param ctx - context exposing the projection registry.
 * @param agent - the agent whose turn is stopping.
 * @param ready - the awaited transcript registration.
 * @param clientNow - builds the client for this call.
 * @param perSession - the session's mutable write-back state.
 * @param signal - the turn's cancellation.
 */
async function writeBack(
  agent: Agent,
  ready: Promise<{ stateOf(session: Session): readonly TranscriptTurn[] }>,
  clientNow: (signal?: AbortSignal) => Promise<WenYuanClient | null>,
  perSession: { recalledTurn: number; retainedTurns: number; writing: Promise<void> | undefined },
  signal: AbortSignal,
): Promise<void> {
  const session = (agent as unknown as { session?: Session }).session
  if (session === undefined) return
  const handle = await ready
  const folded: readonly TranscriptTurn[] = handle.stateOf(session)
  const fresh: TranscriptTurn[] = folded.slice(perSession.retainedTurns)
  if (fresh.length === 0) return
  const client = await clientNow(signal)
  if (client === null) return
  const refId = `conversation:${String(session.id)}`
  const baseTs = fresh[0]?.timestamp ?? new Date().toISOString()
  const content = renderSessionDocument(refId, fresh, baseTs)
  const outcome = await client.retain([{
    content,
    context: RETAIN_CONTEXT,
    document_id: refId,
    tags: ['source:chat', 'harness:dsh'],
    strategy: 'conversation',
    timestamp: baseTs,
    metadata: { source: 'chat', session_id: String(session.id), ref_id: refId, harness: 'dsh' },
  }], signal)
  // Only an accepted write advances the cursor. The projection's state stays a
  // pure fold of the log, so the "what did I already hand over" answer lives
  // beside it, on the session's in-memory state.
  if (outcome.ok) perSession.retainedTurns = folded.length
}

/**
 * One tool's canonical output envelope.
 * @param text - the model-facing text.
 * @returns a lossless-JSON value matching the output schema below.
 */
function toolValue(text: string): { text: string } {
  return { text }
}

/** The output schema every tool here declares: one text field. */
const TOOL_OUTPUT_SCHEMA = {
  type: 'object',
  properties: { text: { type: 'string' } },
  required: ['text'],
  additionalProperties: false,
} as const

/** A tool's shared shape: one text output, rendered verbatim. */
function textTool(
  name: string,
  description: string,
  parameters: Record<string, unknown>,
  run: (args: Record<string, unknown>, exec: ToolRunContext) => Promise<string>,
): ToolDefinition {
  return {
    name,
    description,
    parameters,
    output: {
      schema: TOOL_OUTPUT_SCHEMA,
      render: (_args: unknown, value: unknown) => [
        { type: 'text', text: String((value as { text?: unknown }).text ?? '') },
      ],
    },
    async execute(args: unknown, exec: ToolRunContext): Promise<unknown> {
      const record = args !== null && typeof args === 'object' ? (args as Record<string, unknown>) : {}
      return toolValue(await run(record, exec))
    },
  } as unknown as ToolDefinition
}

/** Read one string argument, trimmed; a blank reads as absent. */
function textArg(args: Record<string, unknown>, key: string): string | undefined {
  const raw = args[key]
  if (typeof raw !== 'string') return undefined
  const trimmed = raw.trim()
  return trimmed === '' ? undefined : trimmed
}

/** The indentation one tree level adds. */
const TREE_INDENT = '  '

/** Render one node and its children as an indented list. */
function renderNode(node: KnowledgeTreeNode, depth: number, lines: string[]): void {
  const indent = TREE_INDENT.repeat(depth)
  lines.push(`${indent}- ${node.name} (\`${node.id}\`)`)
  const children = (node as KnowledgeTreeFolder).children ?? []
  for (const child of children) renderNode(child, depth + 1, lines)
}

/**
 * The five knowledge tools: search, list, and read the bank's curated
 * knowledge pages, plus the two writes that keep them current.
 *
 * The reads answer from knowledge pages — living documents the service
 * synthesizes from memories — rather than from raw memories, which is what
 * separates them from the automatic recall this plugin already injects.
 * @param clientNow - resolves a client for the current settings, or null when
 *   memory is disabled in this deployment.
 * @returns the tool definitions, in registration order.
 */
export function knowledgeTools(
  clientNow: (signal?: AbortSignal) => Promise<WenYuanClient | null>,
): readonly ToolDefinition[] {
  /** Resolve a client or report the one reason every tool shares. */
  const requireClient = async (exec: ToolRunContext): Promise<WenYuanClient | string> => {
    const client = await clientNow(exec.signal)
    return client === null ? 'WenYuan memory is disabled in this deployment.' : client
  }

  return [
    textTool(
      'wenyuan_search_knowledge_pages',
      'Search what this project has already settled, as curated knowledge pages rather than raw memories. Use this first when a question may have been decided before; read a hit in full with wenyuan_read_knowledge_page.',
      {
        type: 'object',
        properties: {
          query: { type: 'string', description: 'What to search for.' },
          limit: { type: 'number', description: 'Maximum pages to return (1-50, default: 10).' },
        },
        required: ['query'],
        additionalProperties: false,
      },
      async (args, exec) => {
        const query = textArg(args, 'query')
        if (query === undefined) return 'wenyuan_search_knowledge_pages needs a non-empty query.'
        const raw = args['limit']
        const limit = typeof raw === 'number' && Number.isFinite(raw) ? Math.min(50, Math.max(1, Math.trunc(raw))) : 10
        const client = await requireClient(exec)
        if (typeof client === 'string') return client
        const outcome = await client.searchKnowledge(query, limit, exec.signal)
        if (!outcome.ok) return `WenYuan knowledge search failed: ${outcome.reason}`
        if (outcome.value.results.length === 0) return 'No knowledge page matched.'
        return outcome.value.results
          .map((hit) => [`- ${hit.name} (\`${hit.id}\`)`, hit.snippet ?? hit.description ?? ''].filter((part) => part !== '').join('\n  '))
          .join('\n')
      },
    ),

    textTool(
      'wenyuan_list_knowledge_pages',
      'List this project\'s knowledge pages as a tree of folders and pages, with the one-line description of what each covers. Use it to see what the bank documents before reading or searching.',
      { type: 'object', properties: {}, additionalProperties: false },
      async (_args, exec) => {
        const client = await requireClient(exec)
        if (typeof client === 'string') return client
        const outcome = await client.listKnowledgePages(exec.signal)
        if (!outcome.ok) return `WenYuan knowledge listing failed: ${outcome.reason}`
        if (outcome.value.roots.length === 0) return 'The bank has no knowledge pages yet.'
        const lines: string[] = []
        for (const root of outcome.value.roots) renderNode(root, 0, lines)
        return lines.join('\n')
      },
    ),

    textTool(
      'wenyuan_read_knowledge_page',
      'Read one knowledge page in full. Get its id from wenyuan_list_knowledge_pages or wenyuan_search_knowledge_pages.',
      {
        type: 'object',
        properties: { page_id: { type: 'string', description: 'The id of the page to read.' } },
        required: ['page_id'],
        additionalProperties: false,
      },
      async (args, exec) => {
        const pageId = textArg(args, 'page_id')
        if (pageId === undefined) return 'wenyuan_read_knowledge_page needs a non-empty page_id.'
        const client = await requireClient(exec)
        if (typeof client === 'string') return client
        const outcome = await client.readKnowledgePage(pageId, exec.signal)
        if (!outcome.ok) return `WenYuan knowledge read failed: ${outcome.reason}`
        const page = outcome.value
        const header = [`# ${page.name}`, `id: \`${page.id}\``]
        if (page.description !== undefined) header.push(page.description)
        if (page.last_updated_at !== undefined) header.push(`last updated: ${page.last_updated_at}`)
        return [...header, '', page.body ?? page.markdown ?? ''].join('\n')
      },
    ),

    textTool(
      'wenyuan_capture_initiative',
      'Record a new initiative as a tracked knowledge page, so future sessions know it exists and can build on it. Call it right after the user approves a plan, before writing code. Call it again with relates_to_page_id when the goal, scope, or rationale materially changes, so the page tracks the current plan. Skip bug fixes, small tweaks, refactors, and chores.',
      {
        type: 'object',
        properties: {
          title: { type: 'string', description: 'Short, specific name of the initiative.' },
          summary: { type: 'string', description: 'Two or three sentences on what is being built and why — the current intent, not the opening plan.' },
          relates_to_page_id: { type: 'string', description: 'An existing initiative page to fold this change into, instead of a new page.' },
        },
        required: ['title', 'summary'],
        additionalProperties: false,
      },
      async (args, exec) => {
        const title = textArg(args, 'title')
        const summary = textArg(args, 'summary')
        if (title === undefined) return 'wenyuan_capture_initiative needs a non-empty title.'
        if (summary === undefined) return 'wenyuan_capture_initiative needs a non-empty summary.'
        const client = await requireClient(exec)
        if (typeof client === 'string') return client
        const content = `# ${title}\n\n${summary}`
        const sessionId = exec.agent === undefined ? 'anonymous' : String(exec.agent.id)
        const outcome = await client.retain([{
          content,
          document_id: `initiative:${sessionId}:${Date.now()}`,
          tags: ['kind:initiative', 'source:chat', 'harness:dsh'],
          strategy: 'conversation',
          timestamp: new Date().toISOString(),
          metadata: { source: 'initiative', title, session_id: sessionId },
        }], exec.signal)
        if (!outcome.ok) return `wenyuan_capture_initiative failed: ${outcome.reason}`
        const ref = outcome.operationId === undefined ? '' : ` (operation ${outcome.operationId})`
        return `Captured initiative "${title}"${ref}. The service derives its knowledge page from memory.`
      },
    ),

    textTool(
      'wenyuan_ingest_document',
      'Save an external document or a block of durable notes into this project\'s memory, so it informs future recall and knowledge pages. Use it for design notes, research, or reference material you want remembered — not for the conversation you are already in, which is captured automatically at session end. This is also the correction mechanism: to supersede a memory you verified as wrong, ingest a document titled "Correction: <topic>" stating what the memory claimed, what is true now, and the evidence.',
      {
        type: 'object',
        properties: {
          title: { type: 'string', description: 'The document\'s title.' },
          content: { type: 'string', description: 'The document body.' },
        },
        required: ['title', 'content'],
        additionalProperties: false,
      },
      async (args, exec) => {
        const title = textArg(args, 'title')
        const content = textArg(args, 'content')
        if (title === undefined) return 'wenyuan_ingest_document needs a non-empty title.'
        if (content === undefined) return 'wenyuan_ingest_document needs non-empty content.'
        const client = await requireClient(exec)
        if (typeof client === 'string') return client
        const sessionId = exec.agent === undefined ? 'anonymous' : String(exec.agent.id)
        const outcome = await client.retain([{
          content: `# ${title}\n\n${content}`,
          document_id: `document:${sessionId}:${Date.now()}`,
          tags: ['kind:document', 'source:chat', 'harness:dsh'],
          strategy: 'conversation',
          timestamp: new Date().toISOString(),
          metadata: { source: 'document', title, session_id: sessionId },
        }], exec.signal)
        if (!outcome.ok) return `wenyuan_ingest_document failed: ${outcome.reason}`
        const ref = outcome.operationId === undefined ? '' : ` (operation ${outcome.operationId})`
        return `Stored "${title}" in WenYuan memory${ref}.`
      },
    ),
  ]
}
