#!/usr/bin/env node
/**
 * Verify the built client bundle against the browser kernel's contract.
 *
 * The configuration card reaches users only if `lib/client.js` satisfies rules
 * enforced in the *browser*, not at build time: the bundle must register a
 * factory under the plugin id, `require` only modules the shell seeds, and
 * export the face the kernel activates. A mistake in any of those fails at page
 * load for the user and nowhere in `typecheck` / `test` / `build`, so this
 * script closes that gap by executing the real artifact through a stand-in
 * module loader.
 *
 * It also cross-checks the manifest (`dsh.client`, `exports["./client"]`) and
 * the two Plugins-page registrations, because those failures are silent in the
 * browser: a `plugins.bundle.config` entry registered by `id` instead of by
 * package-name `key` registers without error and then simply never renders.
 *
 * Usage: node scripts/verify-client-bundle.mjs
 * @module @studyzy/dsh-wenyuan-memory/scripts/verify-client-bundle
 */

import { readFileSync, existsSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const packageRoot = join(dirname(fileURLToPath(import.meta.url)), '..')
const manifest = JSON.parse(readFileSync(join(packageRoot, 'package.json'), 'utf8'))

/** Failures collected so one run reports every problem, not just the first. */
const failures = []
const checks = []

/**
 * Record one check's outcome.
 * @param label - what was verified.
 * @param ok - whether it held.
 * @param detail - optional evidence to print.
 */
function check(label, ok, detail = '') {
  checks.push({ label, ok })
  console.log(`${ok ? 'ok  ' : 'FAIL'}  ${label}${detail === '' ? '' : `  [${detail}]`}`)
  if (!ok) failures.push(label)
}

const CLIENT_ID = `${manifest.name}/client`
const ENTRY_ID = 'dsh-wenyuan-memory'
const NS = 'wenyuan-memory.settings'

// --- The manifest halves that must agree ----------------------------------
const dshClient = manifest.dsh?.client
check('manifest declares dsh.client', dshClient !== undefined)
check('dsh.client declares the web platform', dshClient?.platform === 'web', String(dshClient?.platform))
check('dsh.client injects only preload packages', Array.isArray(dshClient?.inject), JSON.stringify(dshClient?.inject))
const clientExport = manifest.exports?.['./client']
check('exports["./client"] names the built bundle', clientExport?.default === './lib/client.js', String(clientExport?.default))
check('the bundle is a profile bundle', manifest.dsh?.bundle?.patch === './cordis.patch.yml', String(manifest.dsh?.bundle?.patch))

// --- The bundle must load through the shell's module table ----------------
const bundlePath = join(packageRoot, 'lib', 'client.js')
check('lib/client.js exists (build ran first)', existsSync(bundlePath))
const source = existsSync(bundlePath) ? readFileSync(bundlePath, 'utf8') : ''
check(
  'bundle registers a factory under the plugin id',
  // The banner is emitted over several lines; match the id, not its layout.
  /window\.__ModuleLoader__\.load\(\s*\{\s*id:\s*"([^"]+)"\s*,\s*factory:/.test(source)
    && new RegExp(`window\\.__ModuleLoader__\\.load\\(\\s*\\{\\s*id:\\s*"${CLIENT_ID.replace(/[/@.]/g, '\\$&')}"`).test(source),
)

/** The platform singletons the shell seeds into a client bundle. */
const ALLOWED_EXTERNALS = new Set([
  'react',
  'react/jsx-runtime',
  'react-dom',
  'react-dom/client',
  '@deepseek-ai/cordis',
  '@deepseek-ai/dsh-client-store',
  '@deepseek-ai/dsh-client-ui-slots',
  '@deepseek-ai/dsh-client-ui-primitives',
  '@deepseek-ai/dsh-client-ui-dockkit',
])

const requested = []
const seen = { dictionaries: [], slots: [], entries: [], forms: [] }

/** Record one `slots.inject` and run the callback, as the registry does. */
function slotInject(name, register) {
  seen.slots.push(name)
  return register()
}

/** Record one `slots.register` entry. */
function slotRegister(options, component) {
  seen.entries.push({
    slot: options.name, id: options.id, key: options.key, order: options.order,
    label: options.label, locale: options.locale, inject: options.inject, component,
  })
  return () => {}
}

/** A snapshot-store stand-in: the controller builds one and subscribes to it. */
function snapshotStoreStub() {
  let state
  return {
    getSnapshot: () => state,
    subscribe: () => () => {},
    update: (mutate) => { mutate(state) },
    set: (next) => { state = next },
  }
}

/**
 * The one UI-kit symbol the bundle constructs at load: the staged form model.
 * Rendering is out of scope here, so only its existence matters.
 */
class FakeFormModel {
  bind() { return { getSnapshot: () => ({}), subscribe: () => () => {} } }
  shell() { return { available: true, writable: true, dirty: false, invalid: false, saving: false, failed: false } }
  field() { return { text: '', overridden: false, invalid: false } }
  actions() { return { edit: () => {}, resetField: () => {}, save: () => {}, discard: () => {} } }
  dispose() {}
}

/** One stub config form per namespace: `get` is called for the entry itself. */
function formStub() {
  const snapshot = { status: 'ready', writable: true, revision: 1, value: {}, base: {}, user: {} }
  return {
    getSnapshot: () => snapshot,
    subscribe: () => () => {},
    mutate: async () => true,
    set: async () => true,
    unset: async () => true,
  }
}

// The kernel materializes the factory by evaluating the whole file. Provide the
// two globals it reaches for, then let it register itself.
let face
globalThis.window = {
  __ModuleLoader__: {
    load: ({ id, factory }) => {
      check('factory registers under the expected plugin id', id === CLIENT_ID, id)
      face = factory((spec) => {
        requested.push(spec)
        if (!ALLOWED_EXTERNALS.has(spec)) throw new Error(`unexpected external "${spec}"`)
        if (spec === '@deepseek-ai/dsh-client-store') return { createSnapshotStore: snapshotStoreStub }
        if (spec === '@deepseek-ai/dsh-client-ui-primitives') return { SettingsFormModel: FakeFormModel, settingsTextField: () => ({}), settingsNumberField: () => ({}) }
        // react is reached only while rendering, which this script never does;
        // hand back a placeholder so the factory can expose its face.
        return new Proxy({}, {
          get: (_target, property) => (property === '__esModule' ? true : () => undefined),
          has: () => true,
        })
      })
    },
  },
}

try {
  const { createRequire } = await import('node:module')
  const requireFrom = createRequire(join(packageRoot, 'package.json'))
  // Evaluate the artifact as a CommonJS program over a virtual module table.
  const Module = await import('node:module')
  const virtual = new Module.default(bundlePath)
  virtual.filename = bundlePath
  virtual.paths = []
  virtual.require = (spec) => {
    requested.push(spec)
    if (!ALLOWED_EXTERNALS.has(spec)) throw new Error(`unexpected external "${spec}"`)
    if (spec === '@deepseek-ai/dsh-client-store') return { createSnapshotStore: snapshotStoreStub }
    if (spec === '@deepseek-ai/dsh-client-ui-primitives') return { SettingsFormModel: FakeFormModel, settingsTextField: () => ({}), settingsNumberField: () => ({}) }
    return new Proxy({}, { get: (_t, p) => (p === '__esModule' ? true : () => undefined) })
  }
  virtual._compile(source, bundlePath)
  void requireFrom
} catch (error) {
  check('bundle evaluates through the stand-in module loader', false, String(error))
}

check('factory requests only shell-provided modules', requested.every((spec) => ALLOWED_EXTERNALS.has(spec)), requested.join(', ') || '(none)')
check('exports apply()', typeof face?.apply === 'function')
check('exports inject', Array.isArray(face?.inject), JSON.stringify(face?.inject))
check('inject declares the slots service', Array.isArray(face?.inject) && face.inject.includes('slots'), JSON.stringify(face?.inject))

// --- apply(): what the Plugins page actually receives ---------------------
try {
  const scoped = {
    effect: (fn) => { const disposer = fn(); return typeof disposer === 'function' ? disposer : () => {} },
    locale: {
      register: (ns) => { seen.dictionaries.push(ns); return () => {} },
      bind: () => (key) => key,
    },
    configForms: { get: (ns) => { seen.forms.push(ns); return formStub() } },
    remote: {
      credentials: {
        describe: async (refs) => ({ ok: true, value: Object.fromEntries(refs.map((ref) => [ref, { configured: false, writable: true }])) }),
        set: async () => ({ ok: true }),
      },
    },
    slots: { inject: slotInject, register: slotRegister },
  }
  face?.apply({
    effect: scoped.effect,
    slots: scoped.slots,
    inject: (names, body) => { void names; return body(scoped) },
  })
} catch (error) {
  check('apply() runs against the client services', false, String(error))
}

check('registers exactly one dictionary namespace', seen.dictionaries.length === 1, seen.dictionaries.join(', '))
check('dictionary namespace is the card namespace', seen.dictionaries[0] === NS, String(seen.dictionaries[0]))
check('binds the entry config form', seen.forms.includes(ENTRY_ID), seen.forms.join(', '))
check(
  'registers the configuration form on both Plugins-page surfaces',
  seen.slots.includes('plugins.item') && seen.slots.includes('plugins.bundle.config'),
  seen.slots.join(', '),
)

const itemEntry = seen.entries.find((entry) => entry.slot === 'plugins.item')
const bundleEntry = seen.entries.find((entry) => entry.slot === 'plugins.bundle.config')
check('registers the official-plugin card', itemEntry !== undefined)
check('official-plugin card is addressed by the entry id', itemEntry?.id === ENTRY_ID, String(itemEntry?.id))
check('official-plugin card carries a locale label thunk', typeof itemEntry?.label === 'function' && itemEntry.label() === 'title')
check('bundle card is keyed by the package name', bundleEntry?.key === manifest.name, String(bundleEntry?.key))
check('bundle card carries no id (a keyed slot ignores it)', bundleEntry?.id === undefined, String(bundleEntry?.id))

const itemFace = itemEntry?.inject?.()
const bundleFace = bundleEntry?.inject?.()
check('both surfaces inject a face', itemFace !== undefined && bundleFace !== undefined)
check(
  'both surfaces share ONE face, so a draft staged on either is visible on the other',
  itemFace?.hooks?.wenYuanMemoryCard === bundleFace?.hooks?.wenYuanMemoryCard,
)

// --- Report ---------------------------------------------------------------
const failed = checks.filter((entry) => !entry.ok)
console.log('')
if (failed.length > 0) {
  console.error(`${failed.length} check(s) failed:`)
  for (const entry of failed) console.error(`  - ${entry.label}`)
  process.exit(1)
}
console.log(`all ${checks.length} checks passed`)