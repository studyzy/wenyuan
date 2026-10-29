/**
 * The transcript a write-back carries, in the shape the reference's Stop hook
 * produces.
 *
 * The reference reads the host's transcript file and reduces it to a list of
 * `{ role, content, timestamp }` turns: user and assistant text only, plus one
 * `action` line per tool call naming the tool and its target. This module
 * produces that same list from the session events this plugin already has in
 * memory — the difference is only where the turns come from, not what they say.
 *
 * Two rules are load-bearing:
 *
 * - Injected memory is stripped before a turn is kept. Otherwise a recall
 *   attached to turn N is written back as if it were part of turn N, and the
 *   bank starts remembering its own output.
 * - Tool calls become action lines, never their output. The reference caps an
 *   action target at 100 characters for the same reason: a transcript is a
 *   record of what happened, not a second copy of every file it read.
 *
 * Import-free so a test can include it in either program.
 * @module @studyzy/dsh-wenyuan-memory/transcript
 */

import { stripInjectedBlocks } from './context-blocks.ts'

/** One rendered turn of the write-back transcript. */
export interface TranscriptTurn {
  /** `user`, `assistant`, or `action` for one tool call. */
  role: 'user' | 'assistant' | 'action'
  /** The model-facing text, trimmed. */
  content: string
  /** ISO timestamp of the source event, when it has one. */
  timestamp?: string | undefined
}

/** One message block as the session log carries it. */
export interface ContentBlockLike {
  readonly type: string
  readonly text?: string
  readonly name?: string
  readonly input?: unknown
}

/**
 * The keys an action line takes its target from, most specific first — the
 * reference's own ordering, so `Read /a/b` reads the same on both harnesses.
 */
const ACTION_TARGET_KEYS: readonly string[] = [
  'file_path',
  'path',
  'notebook_path',
  'command',
  'pattern',
  'query',
  'url',
  'name',
  'id',
]

/** Cap on an action line's target, matching the reference. */
const ACTION_TARGET_CAP = 100

/**
 * Render one tool call as its action line: the tool name, then the first
 * recognizable target, capped.
 * @param name - the tool name as the model called it.
 * @param input - the call's arguments, if any.
 * @returns the action line.
 */
export function actionLine(name: string, input: unknown): string {
  let target = ''
  if (input !== null && typeof input === 'object') {
    const record = input as Record<string, unknown>
    for (const key of ACTION_TARGET_KEYS) {
      const value = record[key]
      if (typeof value === 'string' && value.trim() !== '') {
        target = value.trim().split('\n')[0] ?? ''
        break
      }
    }
  } else if (typeof input === 'string') {
    target = input.trim().split('\n')[0] ?? ''
  }
  if (target.length > ACTION_TARGET_CAP) target = `${target.slice(0, ACTION_TARGET_CAP)}…`
  return target === '' ? name : `${name} ${target}`
}

/**
 * Render one message's blocks into the turns it contributes.
 *
 * Text joins within the role; each tool call becomes its own action turn after
 * the text, which is the order the reference emits them in. An assistant's
 * reasoning is not carried: it is not part of what happened, and the reference
 * does not read it from the transcript either.
 * @param role - the message's role.
 * @param blocks - the message's model-facing blocks.
 * @returns the rendered turns, possibly empty.
 */
export function renderBlocks(role: 'user' | 'assistant', blocks: readonly ContentBlockLike[]): TranscriptTurn[] {
  const texts: string[] = []
  const actions: TranscriptTurn[] = []
  for (const block of blocks) {
    if (block.type === 'text' && typeof block.text === 'string') {
      const text = stripInjectedBlocks(block.text).trim()
      if (text !== '') texts.push(text)
    } else if (block.type === 'tool-call' && typeof block.name === 'string') {
      actions.push({ role: 'action', content: actionLine(block.name, block.input) })
    }
  }
  const turns: TranscriptTurn[] = []
  const joined = texts.join('\n').trim()
  if (joined !== '') turns.push({ role, content: joined })
  turns.push(...actions)
  return turns
}

/**
 * Convert one event's epoch milliseconds into the ISO timestamp the transcript
 * carries, matching the reference's per-turn `timestamp`.
 * @param time - the event's `time` field.
 * @returns the ISO string, or undefined when the value is not a usable time.
 */
export function isoTimestamp(time: unknown): string | undefined {
  if (typeof time !== 'number' || !Number.isFinite(time) || time <= 0) return undefined
  return new Date(time).toISOString()
}

/**
 * Render the whole session as the reference's transcript document: one JSON
 * turn per line, behind a `REF-ID` line that names the document.
 *
 * The reference writes `{ role: 'system', content: 'REF-ID: …' }` first so a
 * reader can tie the document back to its session, then the turns verbatim.
 * @param refId - the document identity, `conversation:<sessionId>`.
 * @param turns - the rendered turns.
 * @param baseTs - the timestamp the REF-ID line carries.
 * @returns the newline-delimited JSON the retain operation carries.
 */
export function renderSessionDocument(refId: string, turns: readonly TranscriptTurn[], baseTs: string): string {
  const lines: string[] = [JSON.stringify({ role: 'system', content: `REF-ID: ${refId}`, timestamp: baseTs })]
  for (const turn of turns) lines.push(JSON.stringify(turn))
  return lines.join('\n')
}