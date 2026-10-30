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

describe('the bank a call addresses', () => {
  /** Mounted tool by name, over the host fakes. */
  function toolNamed(name: string): { execute(args: unknown, exec: unknown): Promise<unknown> } {
    const { ctx, tools } = fakeContext()
    apply(ctx as never, {})
    return tools.find((candidate) => (candidate as { name: string }).name === name) as {
      execute(args: unknown, exec: unknown): Promise<unknown>
    }
  }

  /** One agent whose session header names a working directory. */
  function agentIn(cwd: string): { id: string; session: { header: { cwd: string } } } {
    return { id: 'session-1', session: { header: { cwd } } }
  }

  /** Answer every request with `body`, recording the URLs it was asked for. */
  async function withFetch(body: unknown, run: (urls: string[], inits: (RequestInit | undefined)[]) => Promise<void>): Promise<void> {
    const originalFetch = globalThis.fetch
    const urls: string[] = []
    const inits: (RequestInit | undefined)[] = []
    globalThis.fetch = (async (url: string, init?: RequestInit) => {
      urls.push(String(url))
      inits.push(init)
      return Response.json(body)
    }) as unknown as typeof fetch
    try {
      await run(urls, inits)
    } finally {
      globalThis.fetch = originalFetch
    }
  }

  it('derives the bank from the calling session\'s working directory', async () => {
    // `auto` is the default, so this is the path every session takes unless
    // the card names a bank: the request must address the project's own bank.
    await withFetch({ results: [] }, async (urls) => {
      await toolNamed('wenyuan_search_knowledge_pages').execute(
        { query: 'anything' },
        { signal: new AbortController().signal, agent: agentIn('/code/studyzy/pvz-shoot') },
      )
      expect(urls[0]).toBe('http://127.0.0.1:18888/v1/default/banks/coding-agent%3A%3Apvz-shoot/knowledge-base/search?q=anything&limit=10')
    })
  })

  it('uses a bank the card names verbatim, whatever the working directory is', async () => {
    const { ctx, tools } = fakeContext()
    apply(ctx as never, { bankId: 'team::shared' })
    const tool = tools.find((candidate) => (candidate as { name: string }).name === 'wenyuan_search_knowledge_pages') as {
      execute(args: unknown, exec: unknown): Promise<unknown>
    }
    await withFetch({ results: [] }, async (urls) => {
      await tool.execute({ query: 'x' }, { signal: new AbortController().signal, agent: agentIn('/code/studyzy/pvz-shoot') })
      expect(urls[0]).toContain('/banks/team%3A%3Ashared/')
    })
  })

  it('falls back to the workspace bank when the call carries no agent', async () => {
    await withFetch({ text: '' }, async (urls) => {
      await toolNamed('wenyuan_reflect').execute({ query: 'x' }, { signal: new AbortController().signal })
      expect(urls[0]).toContain('/banks/coding-agent%3A%3Aworkspace/')
    })
  })

  it('tags a captured initiative with the bank it was written to', async () => {
    await withFetch({}, async (_urls, inits) => {
      await toolNamed('wenyuan_capture_initiative').execute(
        { title: 'Auto banks', summary: 'Derive the bank per project.' },
        { signal: new AbortController().signal, agent: agentIn('/code/studyzy/pvz-shoot') },
      )
      const body = JSON.parse(String(inits[0]?.body)) as { items?: { tags?: string[] }[] }
      expect(body.items?.[0]?.tags).toContain('coding-agent::pvz-shoot:dsh')
    })
  })
})
