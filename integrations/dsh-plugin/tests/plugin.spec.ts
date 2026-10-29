import { describe, expect, it } from 'vitest'
import { DEFAULT_BANK_ID, DEFAULT_API_URL } from '../src/settings.ts'
import { resolveSettings, apply } from '../src/index.ts'

/**
 * The Host plugin's wiring, driven against a stand-in Cordis context.
 *
 * Two behaviours are load-bearing here: the volatile settings are unwrapped
 * (a `volatile()` field arrives as a live `{ get() }` reference, not a value),
 * and the hooks are fail-open — with no credentials service, no memory service,
 * and no session, the plugin must still mount and register its tools rather
 * than throw away its own fiber.
 */

/** One registered listener, as the stand-in records it. */
interface Listener {
  event: string
  handler: (...args: unknown[]) => unknown
}

/** A minimal Cordis stand-in: listeners, tools, projections, effects. */
function fakeContext(options: { credentials?: unknown } = {}) {
  const listeners: Listener[] = []
  const tools: unknown[] = []
  const projections: unknown[] = []
  const services = new Map<string, unknown>([
    ['tools', { register: (definition: unknown) => { tools.push(definition); return () => {} } }],
    ['sessionProjections', { register: (definition: unknown) => { projections.push(definition); return Promise.resolve({ stateOf: () => [] }) } }],
  ])
  if (options.credentials !== undefined) services.set('credentials', options.credentials)
  const ctx = {
    on: (event: string, handler: (...args: unknown[]) => unknown) => { listeners.push({ event, handler }) },
    effect: (fn: () => () => void) => fn(),
    get: (name: string) => services.get(name),
    sessionProjections: services.get('sessionProjections'),
    tools: services.get('tools'),
    inject: (_names: string[], body: (c: unknown) => void) => { body(ctx) },
  }
  return { ctx, listeners, tools, projections }
}

describe('settings resolution', () => {
  it('unwraps a volatile field to the plain value behind it', () => {
    const resolved = resolveSettings({
      apiUrl: { get: () => 'http://example:1' },
      bankId: { get: () => 'mine' },
      injectContext: { get: () => false },
      retainOnStop: { get: () => true },
    } as never)
    expect(resolved).toEqual({
      apiUrl: 'http://example:1', bankId: 'mine', injectContext: false, retainOnStop: true,
    })
  })

  it('falls back to the documented defaults for a blank or absent field', () => {
    expect(resolveSettings({})).toEqual({
      apiUrl: DEFAULT_API_URL, bankId: DEFAULT_BANK_ID, injectContext: true, retainOnStop: true,
    })
    expect(resolveSettings({ apiUrl: '   ', bankId: '' } as never)).toEqual({
      apiUrl: DEFAULT_API_URL, bankId: DEFAULT_BANK_ID, injectContext: true, retainOnStop: true,
    })
  })
})

describe('mounting', () => {
  it('registers the two tools and the transcript projection', () => {
    const { ctx, tools, projections } = fakeContext()
    apply(ctx as never, {})
    expect(tools.map((tool) => (tool as { name: string }).name).sort()).toEqual(['wenyuan_recall', 'wenyuan_retain'])
    expect((projections[0] as { key: string }).key).toBe('wenYuanTranscript')
  })

  it('subscribes to the three agent lifecycle seams and nothing else', () => {
    const { ctx, listeners } = fakeContext()
    apply(ctx as never, {})
    expect(listeners.map((listener) => listener.event).sort()).toEqual([
      'agent/created', 'agent/pre-step', 'agent/turn-stopping',
    ])
  })

  it('mounts without a credentials service rather than failing its own fiber', () => {
    const { ctx, tools } = fakeContext()
    expect(() => apply(ctx as never, {})).not.toThrow()
    expect(tools).toHaveLength(2)
  })
})

describe('a tool with no memory service reachable', () => {
  /**
   * The registered tool definition, run with a credential provider that answers
   * "nothing configured" and a fetch that refuses every connection.
   * @returns the tool's canonical value for one call.
   */
  async function runRecall(): Promise<{ text: string }> {
    const { ctx, tools } = fakeContext({
      credentials: { resolve: async () => undefined },
    })
    apply(ctx as never, {})
    const tool = tools.find((candidate) => (candidate as { name: string }).name === 'wenyuan_recall') as {
      execute(args: unknown, exec: unknown): Promise<unknown>
    }
    const originalFetch = globalThis.fetch
    globalThis.fetch = (async () => { throw new Error('ECONNREFUSED') }) as unknown as typeof fetch
    try {
      return (await tool.execute({ query: 'anything' }, { signal: new AbortController().signal })) as { text: string }
    } finally {
      globalThis.fetch = originalFetch
    }
  }

  it('answers with a failure line instead of throwing into the loop', async () => {
    const value = await runRecall()
    expect(value.text).toContain('WenYuan recall failed')
  })

  it('rejects an empty query before it reaches the network', async () => {
    const { ctx, tools } = fakeContext()
    apply(ctx as never, {})
    const tool = tools.find((candidate) => (candidate as { name: string }).name === 'wenyuan_recall') as {
      execute(args: unknown, exec: unknown): Promise<unknown>
    }
    const value = (await tool.execute({ query: '  ' }, { signal: new AbortController().signal })) as { text: string }
    expect(value.text).toContain('non-empty query')
  })
})