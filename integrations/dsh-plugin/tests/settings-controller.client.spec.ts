// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'

/**
 * The published UI kit's bundled output pulls its own transitive renderer
 * dependencies (`@shikijs/langs`, `anser`, …) at module load, which this
 * package does not own and must not depend on. The controller is exercised as
 * a MODEL, not as a rendered card, so the kit only has to answer the three
 * symbols this module imports: the staged form model and the two field specs.
 * A behaviour test of the real kit belongs to the kit's own suite.
 */
vi.mock('@deepseek-ai/dsh-client-ui-primitives', () => import('./helpers/primitives-stub.ts'))

import { API_KEY_REF, WENYUAN_MEMORY_NS, type WenYuanMemorySettings } from '../src/settings.ts'
import { WenYuanMemoryCardController } from '../src/browser/settings-controller.ts'

/**
 * The card's staged form: the connection settings and the API key.
 *
 * The two rules under test are the ones a user can be lied to about — a URL
 * typo must block the save, and the key must never be read back into a draft
 * (the credentials domain answers "configured", never the value).
 */

/** A scriptable settings scope: a user layer, a base layer, and a write log. */
function makeScope(init: {
  value?: WenYuanMemorySettings
  base?: WenYuanMemorySettings
  user?: WenYuanMemorySettings
  writable?: boolean
  status?: SettingsFormScopeSnapshot<WenYuanMemorySettings>['status']
} = {}): SettingsFormScope<WenYuanMemorySettings> & { writes: unknown[]; accept: boolean } {
  const state: SettingsFormScopeSnapshot<WenYuanMemorySettings> = {
    status: init.status ?? 'ready',
    value: init.value ?? {},
    base: init.base,
    user: init.user,
    writable: init.writable ?? true,
    revision: 1,
  }
  const listeners = new Set<() => void>()
  const writes: unknown[] = []
  const scope = {
    writes,
    accept: true,
    getSnapshot: () => state,
    subscribe: (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener) } },
    mutate: async (ops) => {
      writes.push(ops)
      for (const listener of listeners) listener()
      return scope.accept
    },
  }
  return scope as unknown as SettingsFormScope<WenYuanMemorySettings> & { writes: unknown[]; accept: boolean }
}

/** A remote stand-in answering the credentials namespace. */
function makeRemote(options: { configured?: boolean; writable?: boolean; rejectKey?: string } = {}) {
  const calls: { describe: string[][]; set: [string, string][] } = { describe: [], set: [] }
  const remote = {
    credentials: {
      describe: async (refs: string[]) => {
        calls.describe.push(refs)
        return {
          ok: true as const,
          value: Object.fromEntries(refs.map((ref) => [ref, {
            configured: options.configured ?? false,
            writable: options.writable ?? true,
          }])),
        }
      },
      // A refusal is a `{ ok: false }` result, never a throw: the Host reports
      // its reason in-band, so this is the shape the card has to read.
      set: async (ref: string, value: string) => {
        calls.set.push([ref, value])
        if (options.rejectKey !== undefined) {
          return { ok: false as const, error: { code: 'credential/rejected', message: options.rejectKey } }
        }
        options.configured = true
        return { ok: true as const, value: undefined }
      },
    },
  }
  return { remote, calls }
}

/** The card controller bound to one scope and one remote stand-in. */
function makeCard(scope: ReturnType<typeof makeScope>, remote: ReturnType<typeof makeRemote>) {
  return new WenYuanMemoryCardController(scope, { remote: remote.remote } as never)
}

describe('the card\'s field projection', () => {
  it('shows the effective value and whether the user layer overrides it', () => {
    const scope = makeScope({ value: { apiUrl: 'http://base:1', bankId: 'mine' }, user: { bankId: 'mine' } })
    const card = makeCard(scope, makeRemote())
    const face = card.inject()
    const state = face.hooks.wenYuanMemoryCard.getSnapshot()
    expect(state.apiUrl.text).toBe('http://base:1')
    expect(state.apiUrl.overridden).toBe(false)
    expect(state.bankId.text).toBe('mine')
    expect(state.bankId.overridden).toBe(true)
    expect(state.available).toBe(true)
    card.dispose()
  })

  it('reports the key as configured without ever showing it', async () => {
    const scope = makeScope()
    const remote = makeRemote({ configured: true })
    const card = makeCard(scope, remote)
    await Promise.resolve()
    await Promise.resolve()
    const state = card.inject().hooks.wenYuanMemoryCard.getSnapshot()
    expect(state.apiKeyConfigured).toBe(true)
    expect(state.apiKey.text).toBe('')
    expect(remote.calls.describe[0]).toEqual([API_KEY_REF])
    card.dispose()
  })
})

describe('saving the card', () => {
  it('writes the staged connection settings in one revision-fenced mutation', async () => {
    const scope = makeScope()
    const card = makeCard(scope, makeRemote())
    const face = card.inject()
    face.edit('apiUrl', 'http://wenyuan.example:18888')
    face.edit('bankId', 'team')
    face.save()
    await vi.waitFor(() => { expect(scope.writes).toHaveLength(1) })
    expect(scope.writes[0]).toEqual([
      { op: 'set', path: ['apiUrl'], value: 'http://wenyuan.example:18888' },
      { op: 'set', path: ['bankId'], value: 'team' },
    ])
    card.dispose()
  })

  it('blocks a save whose URL is not an http(s) address', () => {
    const scope = makeScope()
    const card = makeCard(scope, makeRemote())
    const face = card.inject()
    face.edit('apiUrl', 'ftp://nope')
    const state = face.hooks.wenYuanMemoryCard.getSnapshot()
    expect(state.apiUrl.invalid).toBe(true)
    expect(state.invalid).toBe(true)
    card.dispose()
  })

  it('turns an emptied URL into a clear, not an empty write', async () => {
    const scope = makeScope({ value: { apiUrl: 'http://mine:1' }, user: { apiUrl: 'http://mine:1' } })
    const card = makeCard(scope, makeRemote())
    const face = card.inject()
    face.edit('apiUrl', '')
    face.save()
    await vi.waitFor(() => { expect(scope.writes).toHaveLength(1) })
    expect(scope.writes[0]).toEqual([{ op: 'unset', path: ['apiUrl'] }])
    card.dispose()
  })

  it('writes a typed key through the credentials domain and re-reads the configured state', async () => {
    const scope = makeScope()
    const remote = makeRemote({ configured: false })
    const card = makeCard(scope, remote)
    const face = card.inject()
    face.edit('apiKey', 'sk-new')
    face.save()
    await vi.waitFor(() => { expect(remote.calls.set).toEqual([[API_KEY_REF, 'sk-new']]) })
    await vi.waitFor(() => { expect(face.hooks.wenYuanMemoryCard.getSnapshot().apiKeyConfigured).toBe(true) })
    card.dispose()
  })

  it('surfaces a refused key write instead of reporting it as unlanded silently', async () => {
    const scope = makeScope()
    // A refusal is a `{ ok: false }` result, not a throw, and the form contains
    // every throw while flipping one boolean. The reason therefore has to be
    // recorded where the card renders it, which is what this asserts.
    const remote = makeRemote({ configured: false, rejectKey: 'read-only credentials document' })
    const card = makeCard(scope, remote)
    const face = card.inject()
    face.edit('apiKey', 'sk-new')
    face.save()
    await vi.waitFor(() => { expect(remote.calls.set).toEqual([[API_KEY_REF, 'sk-new']]) })
    await vi.waitFor(() => {
      expect(face.hooks.wenYuanMemoryCard.getSnapshot().failure).toContain('read-only credentials document')
    })
    expect(face.hooks.wenYuanMemoryCard.getSnapshot().apiKeyConfigured).toBe(false)
    card.dispose()
  })

  it('stages a switch position as the boolean it will store', async () => {
    const scope = makeScope({ value: { injectContext: true } })
    const card = makeCard(scope, makeRemote())
    const face = card.inject()
    face.setSwitch('injectContext', false)
    const state = face.hooks.wenYuanMemoryCard.getSnapshot()
    expect(state.injectContext.text).toBe('false')
    expect(state.dirty).toBe(true)
    card.dispose()
  })

  it('renders nothing claimable when the namespace is not served', () => {
    const scope = makeScope({ status: 'unavailable' })
    const card = makeCard(scope, makeRemote())
    expect(card.inject().hooks.wenYuanMemoryCard.getSnapshot().available).toBe(false)
    card.dispose()
  })
})

describe('the card\'s settings namespace', () => {
  it('is the profile entry id the bundle patch mounts', () => {
    expect(WENYUAN_MEMORY_NS).toBe('dsh-wenyuan-memory')
  })
})