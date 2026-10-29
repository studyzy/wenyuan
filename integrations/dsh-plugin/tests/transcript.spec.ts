import { describe, expect, it } from 'vitest'
import { actionLine, isoTimestamp, renderBlocks, renderSessionDocument } from '../src/transcript.ts'
import { stripInjectedBlocks } from '../src/context-blocks.ts'

/**
 * The write-back transcript: what a finished session turns into.
 *
 * These cases pin the reference's format, because a write-back that drifts is
 * invisible — the retain succeeds, and the memory the next session recalls is
 * just quietly wrong. The two rules that matter most are here: injected memory
 * never becomes part of what the session "said", and a tool call becomes one
 * action line rather than its output.
 */

describe('injected memory never rides a write-back', () => {
  it('strips a recall block and keeps the text around it', () => {
    const text = 'before\n<wenyuan_memory>\n- remembered thing\n</wenyuan_memory>\nafter'
    expect(stripInjectedBlocks(text)).toBe('before\n\nafter')
  })

  it('strips every reference-side spelling this deployment may carry', () => {
    for (const tag of ['hindsight_memory', 'hindsight_knowledge', 'hindsight_knowledge_refresh', 'hindsight_memory_refresh']) {
      expect(stripInjectedBlocks(`a<${tag}>hidden</${tag}>b`)).toBe('ab')
    }
  })

  it('leaves an ordinary tag of the same prefix alone', () => {
    expect(stripInjectedBlocks('<wenyuan_memories>x</wenyuan_memories>')).toBe('<wenyuan_memories>x</wenyuan_memories>')
  })
})

describe('rendering one message into turns', () => {
  it('joins that message\'s text into one turn, in block order', () => {
    const turns = renderBlocks('user', [
      { type: 'text', text: 'first line' },
      { type: 'text', text: 'second line' },
    ])
    expect(turns).toEqual([{ role: 'user', content: 'first line\nsecond line' }])
  })

  it('renders each tool call as its own action turn after the text', () => {
    const turns = renderBlocks('assistant', [
      { type: 'text', text: 'Let me look.' },
      { type: 'tool-call', name: 'read', input: { file_path: '/code/a.go' } },
      { type: 'tool-call', name: 'grep', input: { pattern: 'func main' } },
    ])
    expect(turns).toEqual([
      { role: 'assistant', content: 'Let me look.' },
      { role: 'action', content: 'read /code/a.go' },
      { role: 'action', content: 'grep func main' },
    ])
  })

  it('drops reasoning blocks and empty text instead of inventing content', () => {
    const turns = renderBlocks('assistant', [
      { type: 'reasoning', text: 'thinking…' },
      { type: 'text', text: '   ' },
      { type: 'tool-call', name: 'bash', input: { command: 'ls' } },
    ])
    expect(turns).toEqual([{ role: 'action', content: 'bash ls' }])
  })

  it('strips a recall block that ended up inside a message', () => {
    const turns = renderBlocks('user', [
      { type: 'text', text: 'real prompt\n<wenyuan_memory>\n- noise\n</wenyuan_memory>' },
    ])
    expect(turns).toEqual([{ role: 'user', content: 'real prompt' }])
  })
})

describe('action lines', () => {
  it('takes the first recognizable target in the reference\'s key order', () => {
    expect(actionLine('edit', { path: '/a', file_path: '/b' })).toBe('edit /b')
    expect(actionLine('webfetch', { url: 'https://example.com/x' })).toBe('webfetch https://example.com/x')
  })

  it('keeps only the first line of a multi-line target', () => {
    expect(actionLine('bash', { command: 'cd /tmp\necho hi' })).toBe('bash cd /tmp')
  })

  it('caps a long target, as the reference does', () => {
    const line = actionLine('bash', { command: 'x'.repeat(200) })
    expect(line).toBe(`bash ${'x'.repeat(100)}…`)
  })

  it('falls back to the tool name alone when no argument names a target', () => {
    expect(actionLine('todowrite', { todos: [] })).toBe('todowrite')
    expect(actionLine('mystery', undefined)).toBe('mystery')
  })
})

describe('the session document', () => {
  it('puts a REF-ID line first, then one JSON turn per line', () => {
    const document = renderSessionDocument('conversation:s1', [
      { role: 'user', content: 'hi', timestamp: '2026-10-28T00:00:00.000Z' },
      { role: 'action', content: 'bash ls' },
    ], '2026-10-28T00:00:00.000Z')
    const lines = document.split('\n')
    expect(JSON.parse(lines[0]!)).toEqual({
      role: 'system',
      content: 'REF-ID: conversation:s1',
      timestamp: '2026-10-28T00:00:00.000Z',
    })
    expect(JSON.parse(lines[1]!)).toEqual({ role: 'user', content: 'hi', timestamp: '2026-10-28T00:00:00.000Z' })
    expect(JSON.parse(lines[2]!)).toEqual({ role: 'action', content: 'bash ls' })
  })

  it('rejects a timestamp the log cannot supply rather than inventing one', () => {
    expect(isoTimestamp(undefined)).toBeUndefined()
    expect(isoTimestamp(0)).toBeUndefined()
    expect(isoTimestamp(Number.NaN)).toBeUndefined()
    expect(isoTimestamp(1_793_171_600_000)).toBe(new Date(1_793_171_600_000).toISOString())
  })
})