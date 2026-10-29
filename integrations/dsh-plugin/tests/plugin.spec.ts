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
  it('registers the eight memory tools and the transcript projection', () => {
    const { ctx, tools, projections } = fakeContext()
    apply(ctx as never, {})
    expect(tools.map((tool) => (tool as { name: string }).name).sort()).toEqual([
      'wenyuan_capture_initiative',
      'wenyuan_diagnose',
      'wenyuan_ingest_document',
      'wenyuan_list_knowledge_pages',
      'wenyuan_read_knowledge_page',
      'wenyuan_reflect',
      'wenyuan_search_knowledge_pages',
      'wenyuan_sync_status',
    ])
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
    expect(tools).toHaveLength(8)
  })
})

describe('a tool with no memory service reachable', () => {
  /**
   * The registered tool definition by name, mounted over the host fakes.
   * @param name - the tool to look up.
   * @returns the tool, ready to execute.
   */
  function toolNamed(name: string): { execute(args: unknown, exec: unknown): Promise<unknown> } {
    const { ctx, tools } = fakeContext()
    apply(ctx as never, {})
    return tools.find((candidate) => (candidate as { name: string }).name === name) as {
      execute(args: unknown, exec: unknown): Promise<unknown>
    }
  }

  /** Run one call with every connection refused. */
  async function refusing<T>(run: () => Promise<T>): Promise<T> {
    const originalFetch = globalThis.fetch
    globalThis.fetch = (async () => { throw new Error('ECONNREFUSED') }) as unknown as typeof fetch
    try {
      return await run()
    } finally {
      globalThis.fetch = originalFetch
    }
  }

  it('answers with a failure line instead of throwing into the loop', async () => {
    const tool = toolNamed('wenyuan_search_knowledge_pages')
    const value = await refusing(async () =>
      (await tool.execute({ query: 'anything' }, { signal: new AbortController().signal })) as { text: string })
    expect(value.text).toContain('WenYuan knowledge search failed')
  })

  it('rejects an empty query before it reaches the network', async () => {
    const tool = toolNamed('wenyuan_search_knowledge_pages')
    const value = (await tool.execute({ query: '  ' }, { signal: new AbortController().signal })) as { text: string }
    expect(value.text).toContain('non-empty query')
  })

  it('rejects a page read with no id before it reaches the network', async () => {
    const tool = toolNamed('wenyuan_read_knowledge_page')
    const value = (await tool.execute({}, { signal: new AbortController().signal })) as { text: string }
    expect(value.text).toContain('non-empty page_id')
  })

  it('rejects an initiative with no summary before it reaches the network', async () => {
    const tool = toolNamed('wenyuan_capture_initiative')
    const value = (await tool.execute({ title: 'x' }, { signal: new AbortController().signal })) as { text: string }
    expect(value.text).toContain('non-empty summary')
  })
})
describe('a tool call that outlives the interactive recall deadline', () => {
  it('waits longer than a lifecycle recall would, so reflect can finish', async () => {
    // Reflect runs a synthesis loop against a model. The lifecycle recall's
    // deadline is what keeps a turn from stalling, and applying it here
    // aborted every reflect that took more than ten seconds.
    const { ctx, tools } = fakeContext()
    apply(ctx as never, {})
    const tool = tools.find((candidate) => (candidate as { name: string }).name === 'wenyuan_reflect') as {
      execute(args: unknown, exec: unknown): Promise<unknown>
    }
    const originalFetch = globalThis.fetch
    let observedInit: RequestInit | undefined
    globalThis.fetch = (async (_url: string, init?: RequestInit) => {
      observedInit = init
      return Response.json({ text: 'answer' })
    }) as unknown as typeof fetch
    try {
      const value = (await tool.execute({ query: 'why' }, { signal: new AbortController().signal })) as { text: string }
      expect(value.text).toBe('answer')
    } finally {
      globalThis.fetch = originalFetch
    }
    expect(observedInit?.method).toBe('POST')
  })
})
