import { describe, expect, it, vi } from 'vitest'
import { textsFromRecall, WenYuanClient } from '../src/client.ts'
import { bankUrl, DEFAULT_BANK_ID, normalizeApiUrl, normalizeBankId } from '../src/settings.ts'

/**
 * The WenYuan HTTP client: the reference's routes, and the fail-open rule.
 *
 * Every failure here must be an outcome, never a throw: a memory service that
 * is down costs the session its memory, not its run.
 */

/** A fetch stand-in that records the requests it answers. */
function stubFetch(answer: (url: string, init: RequestInit | undefined) => Response | Promise<Response>) {
  const calls: { url: string; init: RequestInit | undefined }[] = []
  const impl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    calls.push({ url, init })
    return answer(url, init)
  })
  return { impl: impl as unknown as typeof fetch, calls }
}

const CONNECTION = { apiUrl: 'http://127.0.0.1:18888', bankId: 'default', apiKey: 'sk-test' }

describe('the bank-scoped route prefix', () => {
  it('prefixes every operation with the reference\'s bank path', () => {
    expect(bankUrl(CONNECTION, '/memories/recall')).toBe('http://127.0.0.1:18888/v1/default/banks/default/memories/recall')
  })

  it('encodes a bank id that carries a URL-reserved character', () => {
    expect(bankUrl({ ...CONNECTION, bankId: 'team::shared' }, '')).toBe(
      'http://127.0.0.1:18888/v1/default/banks/team%3A%3Ashared',
    )
  })

  it('normalizes a URL and a blank bank back to their defaults', () => {
    expect(normalizeApiUrl('  http://h:1///  ')).toBe('http://h:1')
    expect(normalizeApiUrl(undefined)).toBe('http://127.0.0.1:18888')
    // A blank bank is the per-session derivation sentinel, not a fixed bank;
    // `resolveBankId` turns it into `coding-agent::<repo>`.
    expect(normalizeBankId('  ')).toBe(DEFAULT_BANK_ID)
    expect(normalizeBankId(' mine ')).toBe('mine')
  })

  it('drops an API prefix the user pasted, so the route is not doubled', () => {
    // `bankUrl` supplies `/v1` itself; a base that already carries it answers
    // 404 on every call, which reads as a bad key rather than a bad URL.
    expect(normalizeApiUrl('http://127.0.0.1:18888/v1')).toBe('http://127.0.0.1:18888')
    expect(normalizeApiUrl('http://127.0.0.1:18888/v1/')).toBe('http://127.0.0.1:18888')
    expect(normalizeApiUrl('https://mem.example.com/V1')).toBe('https://mem.example.com')
    expect(bankUrl({ ...CONNECTION, apiUrl: normalizeApiUrl('http://127.0.0.1:18888/v1') }, '/memories/recall'))
      .toBe('http://127.0.0.1:18888/v1/default/banks/default/memories/recall')
  })
})

describe('recall', () => {
  it('posts the query to the recall route with the bearer key', async () => {
    const { impl, calls } = stubFetch(() => Response.json({ results: [{ text: 'remembered' }] }))
    const client = new WenYuanClient({ connection: CONNECTION, timeoutMs: 1000, fetchImpl: impl })
    const outcome = await client.recall('what do we know?')
    expect(outcome).toEqual({ ok: true, texts: ['remembered'] })
    expect(calls[0]?.url).toBe('http://127.0.0.1:18888/v1/default/banks/default/memories/recall')
    expect(calls[0]?.init?.method).toBe('POST')
    expect((calls[0]?.init?.headers as Record<string, string>)['authorization']).toBe('Bearer sk-test')
    expect(JSON.parse(String(calls[0]?.init?.body))).toEqual({ query: 'what do we know?' })
  })

  it('omits the authorization header when no key is configured', async () => {
    const { impl, calls } = stubFetch(() => Response.json({ results: [] }))
    const client = new WenYuanClient({
      connection: { apiUrl: 'http://h', bankId: 'b' }, timeoutMs: 1000, fetchImpl: impl,
    })
    await client.recall('q')
    expect((calls[0]?.init?.headers as Record<string, string>)['authorization']).toBeUndefined()
  })

  it('resolves an error status to an outcome instead of throwing', async () => {
    const { impl } = stubFetch(() => new Response('boom', { status: 500 }))
    const client = new WenYuanClient({ connection: CONNECTION, timeoutMs: 1000, fetchImpl: impl })
    await expect(client.recall('q')).resolves.toEqual({ ok: false, reason: 'HTTP 500' })
  })

  it('resolves a transport failure to an outcome instead of throwing', async () => {
    const { impl } = stubFetch(() => { throw new Error('ECONNREFUSED') })
    const client = new WenYuanClient({ connection: CONNECTION, timeoutMs: 1000, fetchImpl: impl })
    await expect(client.recall('q')).resolves.toEqual({ ok: false, reason: 'ECONNREFUSED' })
  })

  it('reads both response shapes the reference has used', () => {
    expect(textsFromRecall({ results: [{ text: 'a' }, { text: ' ' }, { text: 'b' }] })).toEqual(['a', 'b'])
    expect(textsFromRecall({ memories: [{ text: 'older' }] })).toEqual(['older'])
    expect(textsFromRecall(null)).toEqual([])
    expect(textsFromRecall({ results: ['not an object'] })).toEqual([])
  })
})

describe('retain', () => {
  it('posts the items asynchronously to the memories route', async () => {
    const { impl, calls } = stubFetch(() => Response.json({ operation_id: 'op-1' }))
    const client = new WenYuanClient({ connection: CONNECTION, timeoutMs: 1000, fetchImpl: impl })
    const outcome = await client.retain([{
      content: 'a fact', document_id: 'conversation:s1', tags: ['source:chat'], strategy: 'conversation',
    }])
    expect(outcome).toEqual({ ok: true, operationId: 'op-1' })
    expect(calls[0]?.url).toBe('http://127.0.0.1:18888/v1/default/banks/default/memories')
    const body = JSON.parse(String(calls[0]?.init?.body)) as { items: unknown[]; async: boolean }
    expect(body.async).toBe(true)
    expect(body.items).toEqual([{
      content: 'a fact', document_id: 'conversation:s1', tags: ['source:chat'], strategy: 'conversation',
    }])
  })

  it('skips the request entirely when there is nothing to retain', async () => {
    const { impl, calls } = stubFetch(() => Response.json({}))
    const client = new WenYuanClient({ connection: CONNECTION, timeoutMs: 1000, fetchImpl: impl })
    await expect(client.retain([])).resolves.toEqual({ ok: true })
    expect(calls).toHaveLength(0)
  })

  it('accepts an empty answer as a successful write', async () => {
    const { impl } = stubFetch(() => new Response('', { status: 202 }))
    const client = new WenYuanClient({ connection: CONNECTION, timeoutMs: 1000, fetchImpl: impl })
    await expect(client.retain([{ content: 'x', document_id: 'd' }])).resolves.toEqual({ ok: true })
  })

  it('reports a refused write instead of throwing', async () => {
    const { impl } = stubFetch(() => new Response('nope', { status: 401 }))
    const client = new WenYuanClient({ connection: CONNECTION, timeoutMs: 1000, fetchImpl: impl })
    await expect(client.retain([{ content: 'x', document_id: 'd' }])).resolves.toEqual({ ok: false, reason: 'HTTP 401' })
  })
})
describe('the knowledge-base reads', () => {
  it('queries the search route with the service\'s own parameter names', async () => {
    const { impl, calls } = stubFetch(() => Response.json({ results: [{ id: 'kp-1', name: 'Core', snippet: 'all of it' }] }))
    const client = new WenYuanClient({ connection: CONNECTION, timeoutMs: 1000, fetchImpl: impl })
    const outcome = await client.searchKnowledge('conventions', 5)
    // The route names the query `q`; `query` is answered 422 by the service.
    expect(calls[0]?.url).toBe(
      'http://127.0.0.1:18888/v1/default/banks/default/knowledge-base/search?q=conventions&limit=5',
    )
    expect(calls[0]?.init?.method).toBe('GET')
    expect(outcome).toEqual({ ok: true, value: { results: [{ id: 'kp-1', name: 'Core', snippet: 'all of it' }] } })
  })

  it('reads the tree and keeps each node\'s page id', async () => {
    const { impl, calls } = stubFetch(() => Response.json({
      roots: [{ id: 'kp-1', kind: 'page', name: 'Core', description: 'the vocabulary' }],
    }))
    const client = new WenYuanClient({ connection: CONNECTION, timeoutMs: 1000, fetchImpl: impl })
    const outcome = await client.listKnowledgePages()
    expect(calls[0]?.url).toBe('http://127.0.0.1:18888/v1/default/banks/default/knowledge-base/tree')
    expect(outcome).toEqual({
      ok: true,
      value: { roots: [{ id: 'kp-1', kind: 'page', name: 'Core', description: 'the vocabulary' }] },
    })
  })

  it('reads one page by id and carries its body', async () => {
    const { impl, calls } = stubFetch(() => Response.json({ id: 'kp-1', name: 'Core', body: '# Core\n\ntext' }))
    const client = new WenYuanClient({ connection: CONNECTION, timeoutMs: 1000, fetchImpl: impl })
    const outcome = await client.readKnowledgePage('kp-1')
    expect(calls[0]?.url).toBe('http://127.0.0.1:18888/v1/default/banks/default/knowledge-base/pages/kp-1')
    expect(outcome).toEqual({ ok: true, value: { id: 'kp-1', name: 'Core', body: '# Core\n\ntext' } })
  })

  it('encodes a page id so a reserved character cannot retarget the route', async () => {
    const { impl, calls } = stubFetch(() => Response.json({ id: 'a/b', name: 'x' }))
    const client = new WenYuanClient({ connection: CONNECTION, timeoutMs: 1000, fetchImpl: impl })
    await client.readKnowledgePage('a/b')
    expect(calls[0]?.url).toBe('http://127.0.0.1:18888/v1/default/banks/default/knowledge-base/pages/a%2Fb')
  })

  it('reports a refused read instead of throwing', async () => {
    const { impl } = stubFetch(() => new Response('nope', { status: 404 }))
    const client = new WenYuanClient({ connection: CONNECTION, timeoutMs: 1000, fetchImpl: impl })
    await expect(client.readKnowledgePage('missing')).resolves.toEqual({ ok: false, reason: 'HTTP 404' })
  })
})

describe('reflect, diagnose, and sync status', () => {
  it('asks the reflect route for a synthesized answer', async () => {
    const { impl, calls } = stubFetch(() => Response.json({ text: '## answer' }))
    const client = new WenYuanClient({ connection: CONNECTION, timeoutMs: 1000, fetchImpl: impl })
    const outcome = await client.reflect('why')
    expect(calls[0]?.url).toBe('http://127.0.0.1:18888/v1/default/banks/default/reflect')
    expect(calls[0]?.init?.method).toBe('POST')
    expect(outcome).toEqual({ ok: true, text: '## answer' })
  })

  it('reports a refused reflect instead of throwing', async () => {
    const { impl } = stubFetch(() => new Response('nope', { status: 504 }))
    const client = new WenYuanClient({ connection: CONNECTION, timeoutMs: 1000, fetchImpl: impl })
    await expect(client.reflect('why')).resolves.toEqual({ ok: false, reason: 'HTTP 504' })
  })

  it('reads the counter sheet, keeping the counters it needs', async () => {
    const { impl, calls } = stubFetch(() => Response.json({
      bank_id: 'b', total_nodes: 12, total_links: 34, total_documents: 5,
      pending_operations: 2, failed_operations: 3,
      nodes_by_fact_type: { world: 7, observation: 5 },
    }))
    const client = new WenYuanClient({ connection: CONNECTION, timeoutMs: 1000, fetchImpl: impl })
    const outcome = await client.stats()
    expect(calls[0]?.url).toBe('http://127.0.0.1:18888/v1/default/banks/default/stats')
    expect(outcome).toEqual({
      ok: true,
      value: {
        bank_id: 'b', total_nodes: 12, total_links: 34, total_documents: 5,
        pending_operations: 2, failed_operations: 3,
        nodes_by_fact_type: { world: 7, observation: 5 },
      },
    })
  })

  it('reads failed operations with the reason each one gave up', async () => {
    const { impl, calls } = stubFetch(() => Response.json({
      total: 1,
      operations: [{ id: 'op-1', operation_id: 'op-1', task_type: 'retain', status: 'failed', error_message: 'attempts exhausted', retry_count: 3 }],
    }))
    const client = new WenYuanClient({ connection: CONNECTION, timeoutMs: 1000, fetchImpl: impl })
    const outcome = await client.operations('failed', 5)
    expect(calls[0]?.url).toBe('http://127.0.0.1:18888/v1/default/banks/default/operations?status=failed&limit=5')
    expect(outcome).toEqual({
      ok: true,
      value: {
        total: 1,
        operations: [{
          operation_id: 'op-1', task_type: 'retain', status: 'failed',
          error_message: 'attempts exhausted', retry_count: 3,
        }],
      },
    })
  })
})
