/**
 * The Host-only `wenYuanTranscript` projection: the turns a write-back carries,
 * folded from the committed session log.
 *
 * `dsh >= 0.2.0` deprecates synchronous history reads for new callers, so the
 * Stop-time write-back cannot scan the session on a whim; it reads a fold the
 * projection registry drives forward over the same committed events. The fold
 * keeps exactly what the reference's Stop hook would have read out of a
 * transcript file — user and assistant text plus one action line per tool call
 * — and nothing else.
 *
 * The state is derived data: a lost or invalid cache only costs the next
 * write-back its history, never a session's readability. It is host-only — the
 * transcript never rides a client snapshot — and it carries no write cursor:
 * "what did I already hand over" is per-process state, not a property of the
 * log, so it would only go stale the moment the service was unreachable.
 * @module @studyzy/dsh-wenyuan-memory/transcript-projection
 */

import { z } from 'zod'
import type { SessionHeader } from '@deepseek-ai/dsh-session'
import type { ProjectionDefinition } from '@deepseek-ai/dsh-session-projection'
import { stripInjectedBlocks } from './context-blocks.ts'
import { actionLine, isoTimestamp, type TranscriptTurn } from './transcript.ts'

/** The projection key this plugin owns. */
export const TRANSCRIPT_KEY = 'wenYuanTranscript'

/** The folded transcript state: the write-back's turns, in order. */
export type WenYuanTranscriptState = readonly TranscriptTurn[]

/** The empty state: a fresh session with nothing to write back yet. */
export const EMPTY_TRANSCRIPT_STATE: WenYuanTranscriptState = []

/** Validates persisted state before it seeds a fold. */
const transcriptStateSchema = z.array(z.object({
  role: z.enum(['user', 'assistant', 'action']),
  content: z.string(),
  timestamp: z.string().optional(),
}))

/** One message's blocks as the fold reads them. */
interface MessageLike {
  readonly content?: readonly { readonly type?: string; readonly text?: unknown; readonly name?: unknown; readonly input?: unknown }[]
}

/**
 * Render one message into the turns it contributes.
 *
 * This mirrors {@link renderBlocks} but reads the session's block shape
 * defensively: the fold sees whatever an earlier build wrote, and a malformed
 * message must cost its own text rather than the whole session.
 * @param role - the message's role.
 * @param message - the message as the event carries it.
 * @param timestamp - the event's ISO timestamp.
 * @returns the rendered turns.
 */
function turnsOf(role: 'user' | 'assistant', message: MessageLike, timestamp: string | undefined): TranscriptTurn[] {
  const blocks = Array.isArray(message.content) ? message.content : []
  const texts: string[] = []
  const actions: TranscriptTurn[] = []
  for (const block of blocks) {
    if (block === null || typeof block !== 'object') continue
    if (block.type === 'text' && typeof block.text === 'string') {
      const text = stripInjectedBlocks(block.text).trim()
      if (text !== '') texts.push(text)
    } else if (block.type === 'tool-call' && typeof block.name === 'string') {
      const action: TranscriptTurn = { role: 'action', content: actionLine(block.name, block.input) }
      actions.push(timestamp === undefined ? action : { ...action, timestamp })
    }
  }
  const turns: TranscriptTurn[] = []
  const joined = texts.join('\n').trim()
  if (joined !== '') turns.push(timestamp === undefined ? { role, content: joined } : { role, content: joined, timestamp })
  turns.push(...actions)
  return turns
}

/**
 * The projection unit.
 *
 * `stateVersion` starts at 1; bump it whenever the folded fields or their
 * meaning change, so a persisted row from an older unit is discarded rather
 * than forward-applied into garbage.
 */
export const wenYuanTranscriptProjection = {
  key: TRANSCRIPT_KEY,
  stateSchema: transcriptStateSchema,
  init: (_header: SessionHeader) => EMPTY_TRANSCRIPT_STATE,
  apply: (state: WenYuanTranscriptState, event): WenYuanTranscriptState => {
    if (event.type !== 'user/message' && event.type !== 'assistant/message') return state
    const data = event.data as { message?: MessageLike; turn?: number; step?: number }
    if (data.message === undefined) return state
    const role: 'user' | 'assistant' = event.type === 'user/message' ? 'user' : 'assistant'
    const appended = turnsOf(role, data.message, isoTimestamp(event.time))
    if (appended.length === 0) return state
    // A new array only when this event contributed something; an uninteresting
    // event must return the same reference so the drive produces no work.
    return [...state, ...appended]
  },
  stateVersion: 1,
} as ProjectionDefinition<typeof TRANSCRIPT_KEY, WenYuanTranscriptState>

declare module '@deepseek-ai/dsh-session-projection/types' {
  interface SessionProjectionStateMap {
    /** The write-back transcript, folded from the committed session log. */
    wenYuanTranscript: WenYuanTranscriptState
  }
}