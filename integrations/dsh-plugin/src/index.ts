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
import { WenYuanClient } from './client.ts'
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

  // --- The two tools. ---
  ctx.effect(() => ctx.tools.register(recallTool(clientNow)), 'dsh-wenyuan-memory: recall tool')
  ctx.effect(() => ctx.tools.register(retainTool(clientNow, resolved.bankId)), 'dsh-wenyuan-memory: retain tool')
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

/** The recall tool's definition. */
function recallTool(clientNow: (signal?: AbortSignal) => Promise<WenYuanClient | null>): ToolDefinition {
  return {
    name: 'wenyuan_recall',
    description: 'Recall what WenYuan memory already knows about this project, the user, or a past decision.',
    parameters: {
      type: 'object',
      properties: { query: { type: 'string', description: 'What to recall.' } },
      required: ['query'],
      additionalProperties: false,
    },
    output: {
      schema: TOOL_OUTPUT_SCHEMA,
      render: (_args: unknown, value: unknown) => [
        { type: 'text', text: String((value as { text?: unknown }).text ?? '') },
      ],
    },
    async execute(args: unknown, exec: ToolRunContext): Promise<unknown> {
      const query = typeof (args as { query?: unknown }).query === 'string' ? (args as { query: string }).query : ''
      if (query.trim() === '') return toolValue('wenyuan_recall needs a non-empty query.')
      const client = await clientNow(exec.signal)
      if (client === null) return toolValue('WenYuan memory is disabled in this deployment.')
      const outcome = await client.recall(query, exec.signal)
      if (!outcome.ok) return toolValue(`WenYuan recall failed: ${outcome.reason}`)
      return toolValue(outcome.texts.length === 0 ? 'No memory matched.' : outcome.texts.map((text) => `- ${text}`).join('\n'))
    },
  } as unknown as ToolDefinition
}

/** The retain tool's definition. */
function retainTool(
  clientNow: (signal?: AbortSignal) => Promise<WenYuanClient | null>,
  bankId: string,
): ToolDefinition {
  return {
    name: 'wenyuan_retain',
    description: 'Store a durable fact, decision or convention in WenYuan memory so later sessions can recall it.',
    parameters: {
      type: 'object',
      properties: {
        content: { type: 'string', description: 'The fact, decision or convention to remember.' },
        tags: { type: 'array', items: { type: 'string' }, description: 'Optional tags for later filtering.' },
      },
      required: ['content'],
      additionalProperties: false,
    },
    output: {
      schema: TOOL_OUTPUT_SCHEMA,
      render: (_args: unknown, value: unknown) => [
        { type: 'text', text: String((value as { text?: unknown }).text ?? '') },
      ],
    },
    async execute(args: unknown, exec: ToolRunContext): Promise<unknown> {
      const content = typeof (args as { content?: unknown }).content === 'string' ? (args as { content: string }).content : ''
      if (content.trim() === '') return toolValue('wenyuan_retain needs non-empty content.')
      const tags = Array.isArray((args as { tags?: unknown }).tags)
        ? ((args as { tags: unknown[] }).tags.filter((tag): tag is string => typeof tag === 'string'))
        : []
      const client = await clientNow(exec.signal)
      if (client === null) return toolValue('WenYuan memory is disabled in this deployment.')
      const sessionId = exec.agent === undefined ? 'anonymous' : String(exec.agent.id)
      const documentId = `tool:${sessionId}:${Date.now()}`
      const outcome = await client.retain([{
        content: content.trim(),
        document_id: documentId,
        tags: [...tags, 'source:chat', 'harness:dsh'],
        strategy: 'conversation',
        timestamp: new Date().toISOString(),
        metadata: { source: 'chat', session_id: sessionId, bank: bankId },
      }], exec.signal)
      if (!outcome.ok) return toolValue(`WenYuan retain failed: ${outcome.reason}`)
      return toolValue('Stored in WenYuan memory.')
    },
  } as unknown as ToolDefinition
}