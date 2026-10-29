/**
 * The install-time dependency declaration, which decides which copies of the
 * harness runtime packages a profile ends up with.
 *
 * The harness resolves its own packages out of the profile's module tree, so a
 * copy this package installs next to itself can end up above the runtime's
 * copy once the profile hoists. The harness then evaluates that copy's
 * exact-version peers, refuses them, and disables the profile rows they back.
 * Every package this plugin imports while the host runs it therefore belongs in
 * `peerDependencies` — by range, never an exact pin — and only in
 * `devDependencies` for the local build.
 */
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

/** The manifest fields this file constrains. */
interface Manifest {
  name: string
  dependencies?: Record<string, string>
  peerDependencies?: Record<string, string>
}

const manifest = JSON.parse(
  readFileSync(fileURLToPath(new URL('../package.json', import.meta.url)), 'utf8'),
) as Manifest

describe('package manifest', () => {
  it('installs no harness runtime package alongside the plugin', () => {
    const installed = Object.keys(manifest.dependencies ?? {})
      .filter((name) => name === '@deepseek-ai/dsh' || name.startsWith('@deepseek-ai/dsh-'))
    expect(installed).toEqual([])
  })

  it('asks the host for each runtime package it imports, by range', () => {
    const hostProvided = Object.keys(manifest.peerDependencies ?? {})
      .filter((name) => name.startsWith('@deepseek-ai/dsh-') || name.startsWith('@deepseek-ai/cordis'))
    expect(hostProvided.length).toBeGreaterThan(0)
    for (const name of hostProvided) {
      const range = manifest.peerDependencies?.[name] ?? ''
      expect(range, `${name} must be declared as a peer`).not.toBe('')
      // An exact version here is the defect this guards: it pins the copy the
      // harness evaluates to the release this repo happens to build against.
      expect(range, `${name} must be a range, not an exact pin`).not.toMatch(/^\d+\.\d+\.\d+/)
    }
  })

  it('declares the browser bundle the shell discovers', () => {
    const raw = JSON.parse(
      readFileSync(fileURLToPath(new URL('../package.json', import.meta.url)), 'utf8'),
    ) as { dsh?: { client?: { platform?: string }, bundle?: { patch?: string } }, exports?: Record<string, { default?: string }> }
    expect(raw.dsh?.client?.platform).toBe('web')
    expect(raw.dsh?.bundle?.patch).toBe('./cordis.patch.yml')
    expect(raw.exports?.['./client']?.default).toBe('./lib/client.js')
  })
})