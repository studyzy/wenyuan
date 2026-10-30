import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { clearRepoCache, gitRepoAt, sessionCwd } from '../src/bank-scope.ts'
import {
  autoBankId, isAutoBankId, normalizeBankId, parseGitRepo, resolveBankId,
} from '../src/settings.ts'
import { resolveSettings } from '../src/index.ts'

/**
 * Which bank a session addresses.
 *
 * One fixed default bank is what this replaces: every repository the user opens
 * shared it, so memory written while working in one project was recalled in the
 * next. The bank now follows the session's working directory — the pure rules
 * are pinned here, and the one IO call that reads a git remote is exercised
 * against real repositories created in a temp directory.
 */

describe('parseGitRepo', () => {
  it('reads the repository out of both remote spellings', () => {
    // The scp-like form a `git clone git@github.com:...` writes.
    expect(parseGitRepo('git@github.com:studyzy/wenyuan.git')).toBe('wenyuan')
    expect(parseGitRepo('git@gitlab.example.com:team/sub/project.git')).toBe('project')
    // The URL-like forms, with and without the `.git` suffix, and with a port.
    expect(parseGitRepo('https://github.com/studyzy/pvz-shoot.git')).toBe('pvz-shoot')
    expect(parseGitRepo('https://github.com/studyzy/pvz-shoot')).toBe('pvz-shoot')
    expect(parseGitRepo('https://github.com/studyzy/pvz-shoot/')).toBe('pvz-shoot')
    expect(parseGitRepo('https://git.example.com:8443/a/b.git')).toBe('b')
    expect(parseGitRepo('ssh://git@host.example.com:2222/owner/repo.git')).toBe('repo')
    expect(parseGitRepo('git://host.example.com/owner/repo.git')).toBe('repo')
    // A local path remote, which is what a bare clone directory records.
    expect(parseGitRepo('/srv/git/wenyuan.git')).toBe('wenyuan')
  })

  it('answers nothing for a value that names no repository', () => {
    expect(parseGitRepo(undefined)).toBeUndefined()
    expect(parseGitRepo('   ')).toBeUndefined()
    expect(parseGitRepo('/')).toBeUndefined()
    expect(parseGitRepo('.git')).toBeUndefined()
  })
})

describe('autoBankId', () => {
  it('prefixes the repository name', () => {
    expect(autoBankId('pvz-shoot')).toBe('coding-agent::pvz-shoot')
  })

  it('falls back to the workspace bank when no name is usable', () => {
    expect(autoBankId(undefined)).toBe('coding-agent::workspace')
    expect(autoBankId('  ')).toBe('coding-agent::workspace')
    expect(autoBankId('.')).toBe('coding-agent::workspace')
    expect(autoBankId('..')).toBe('coding-agent::workspace')
    expect(autoBankId('a/b')).toBe('coding-agent::workspace')
  })
})

describe('resolveBankId', () => {
  it('uses a genuinely explicit bank verbatim', () => {
    // A deployment that names a bank to share across projects must keep
    // working — anything that is not a sentinel spelling is taken as written.
    expect(resolveBankId('team::shared', { cwd: '/code/x/pvz-shoot' })).toBe('team::shared')
    expect(resolveBankId(' mine ', {})).toBe('mine')
  })

  it('derives for the legacy `default` spelling a stored card still holds', () => {
    // `default` was the sentinel while the bank was one fixed name. A card that
    // saved it keeps that literal in its stored settings, and no deployment
    // provisions a bank called `default`, so reading it verbatim made every
    // request a 404. It now derives like `auto` does.
    expect(resolveBankId('default', { cwd: '/code/x/wenyuan', repo: 'wenyuan' })).toBe('coding-agent::wenyuan')
    expect(resolveBankId('Default', { cwd: '/code/x/pvz-shoot' })).toBe('coding-agent::pvz-shoot')
    expect(resolveBankId('default')).toBe('coding-agent::workspace')
  })

  it('derives from the repository of the working directory when asked to auto', () => {
    expect(resolveBankId('auto', { cwd: '/code/wenyuan', repo: 'wenyuan' })).toBe('coding-agent::wenyuan')
    expect(resolveBankId('', { cwd: '/code/wenyuan', repo: 'wenyuan' })).toBe('coding-agent::wenyuan')
    expect(resolveBankId(undefined, { cwd: '/code/wenyuan', repo: 'wenyuan' })).toBe('coding-agent::wenyuan')
    expect(resolveBankId('AUTO', { cwd: '/code/wenyuan', repo: 'wenyuan' })).toBe('coding-agent::wenyuan')
  })

  it('falls back to the working directory\'s own name without a remote', () => {
    expect(resolveBankId('auto', { cwd: '/code/studyzy/pvz-shoot' })).toBe('coding-agent::pvz-shoot')
    // A trailing separator is not a directory name.
    expect(resolveBankId('auto', { cwd: '/code/studyzy/pvz-shoot/' })).toBe('coding-agent::pvz-shoot')
    expect(resolveBankId('auto', { cwd: 'C:\\code\\pvz-shoot' })).toBe('coding-agent::pvz-shoot')
  })

  it('falls back to the workspace bank when the session has no working directory', () => {
    expect(resolveBankId('auto')).toBe('coding-agent::workspace')
    expect(resolveBankId('auto', {})).toBe('coding-agent::workspace')
    expect(resolveBankId('auto', { cwd: '   ' })).toBe('coding-agent::workspace')
    expect(resolveBankId(undefined, { cwd: '/' })).toBe('coding-agent::workspace')
  })
})

describe('the setting\'s own default', () => {
  it('is the auto sentinel, and a blank normalizes to it', () => {
    expect(resolveSettings({}).bankId).toBe('auto')
    expect(resolveSettings({ bankId: '  ' } as never).bankId).toBe('auto')
    expect(normalizeBankId('')).toBe('auto')
    expect(isAutoBankId('auto')).toBe(true)
    expect(isAutoBankId('default')).toBe(true)
    expect(isAutoBankId('DEFAULT')).toBe(true)
    expect(isAutoBankId('coding-agent::wenyuan')).toBe(false)
  })
})

describe('reading the repository off a real working directory', () => {
  const sandboxes: string[] = []

  /** One throwaway directory, removed after the test. */
  function sandbox(): string {
    const dir = mkdtempSync(join(tmpdir(), 'wenyuan-bank-scope-'))
    sandboxes.push(dir)
    return dir
  }

  /** Make the directory a repository whose origin is `url`. */
  function makeRepo(cwd: string, url: string): void {
    execFileSync('git', ['init', '--quiet'], { cwd, stdio: 'ignore' })
    execFileSync('git', ['remote', 'add', 'origin', url], { cwd, stdio: 'ignore' })
  }

  beforeEach(() => { clearRepoCache() })

  afterEach(() => {
    clearRepoCache()
    for (const dir of sandboxes.splice(0)) rmSync(dir, { recursive: true, force: true })
  })

  it('answers the repository named by the ssh remote', async () => {
    const cwd = sandbox()
    makeRepo(cwd, 'git@github.com:studyzy/pvz-shoot.git')
    expect(await gitRepoAt(cwd)).toBe('pvz-shoot')
    expect(resolveBankId('auto', { cwd, repo: await gitRepoAt(cwd) })).toBe('coding-agent::pvz-shoot')
  }, 30_000)

  it('answers the repository named by the https remote', async () => {
    const cwd = sandbox()
    makeRepo(cwd, 'https://github.com/studyzy/wenyuan.git')
    expect(await gitRepoAt(cwd)).toBe('wenyuan')
  }, 30_000)

  it('answers nothing for a directory that is not a repository', async () => {
    const cwd = sandbox()
    expect(await gitRepoAt(cwd)).toBeUndefined()
    // The derivation still works: the directory's own name is the bank.
    expect(resolveBankId('auto', { cwd, repo: await gitRepoAt(cwd) })).toBe(
      `coding-agent::${cwd.split('/').at(-1) ?? ''}`,
    )
  }, 30_000)

  it('reuses the cached answer instead of asking git again', async () => {
    const cwd = sandbox()
    makeRepo(cwd, 'git@github.com:studyzy/pvz-shoot.git')
    expect(await gitRepoAt(cwd)).toBe('pvz-shoot')
    // A remote re-pointed inside the cache window is not re-read: the answer
    // is cached per directory, which is what keeps a recall per step cheap.
    execFileSync('git', ['remote', 'set-url', 'origin', 'git@github.com:studyzy/other.git'], { cwd, stdio: 'ignore' })
    expect(await gitRepoAt(cwd)).toBe('pvz-shoot')
    clearRepoCache()
    expect(await gitRepoAt(cwd)).toBe('other')
  }, 30_000)

  it('answers nothing for a session with no working directory', async () => {
    expect(await gitRepoAt(undefined)).toBeUndefined()
    expect(sessionCwd(undefined)).toBeUndefined()
  })

  it('reads the working directory off the session header', () => {
    const agent = { session: { header: { cwd: '/code/studyzy/pvz-shoot' } } }
    expect(sessionCwd(agent as never)).toBe('/code/studyzy/pvz-shoot')
    expect(sessionCwd({ session: { header: {} } } as never)).toBeUndefined()
  })
})

describe('a repository that cannot be read', () => {
  it('leaves the bank keyed by the directory, never by an error', () => {
    // A cwd that does not exist is what a deleted checkout looks like: git
    // fails, the resolver is handed no repo, and the directory name stands in.
    expect(resolveBankId('auto', { cwd: '/nonexistent/checkout/pvz-shoot', repo: undefined }))
      .toBe('coding-agent::pvz-shoot')
  })

  it('does not let a failing spawn throw out of the reader', async () => {
    clearRepoCache()
    // `gitRepoAt` is called with a directory git cannot read; the answer is
    // "none" rather than a rejection that would lose the session its memory.
    await expect(gitRepoAt(join(tmpdir(), 'wenyuan-missing-dir-' + String(process.pid)))).resolves.toBeUndefined()
    clearRepoCache()
  })
})