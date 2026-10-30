/**
 * Where a session's automatic memory bank comes from: its working directory,
 * and the repository that directory belongs to.
 *
 * {@link resolveBankId} in `settings.ts` is the pure half — it states what a
 * bank id is for a given cwd and repository — and this module is the Host half
 * that reads those two facts. It is kept apart because it owns IO: reading the
 * git remote spawns a process, and the browser bundle must be able to import the
 * settings module without dragging that in.
 *
 * The lookup is fail-open in every direction: no session, no working directory,
 * no git binary, no remote, or a remote this module cannot parse each resolve to
 * "no repository", which leaves the bank keyed by the directory's own name. A
 * session must never lose its memory to a repository it cannot name.
 * @module @studyzy/dsh-wenyuan-memory/bank-scope
 */

import { execFile } from 'node:child_process'
import type { Agent } from '@deepseek-ai/dsh-agent'
import type { Session } from '@deepseek-ai/dsh-session'
import { parseGitRepo } from './settings.ts'

/**
 * How long one working directory's repository answer is reused. A session does
 * not change repositories mid-turn, and a recall per step would otherwise spawn
 * a git process per step; a turn-scale window keeps a re-pointed remote
 * reachable without a restart.
 */
const REMOTE_TTL_MS = 5 * 60_000

/** The deadline for reading one remote: a hook is waiting on it. */
const GIT_TIMEOUT_MS = 1_500

/** One cached answer: the repository name, or undefined for "none readable". */
interface RemoteEntry {
  /** The parsed repository name, absent when the directory has none. */
  repo: string | undefined
  /** When the answer was read, for {@link REMOTE_TTL_MS}. */
  readAt: number
}

/** Per-directory remote answers, keyed by absolute cwd. */
const remotes = new Map<string, RemoteEntry>()

/**
 * The working directory a session was created in.
 * @param agent - the agent whose session to read, possibly absent.
 * @returns the absolute cwd, or undefined for a session that carries none.
 */
export function sessionCwd(agent: Agent | undefined): string | undefined {
  const cwd = (agent as unknown as { session?: Session } | undefined)?.session?.header?.cwd
  return typeof cwd === 'string' && cwd.trim() !== '' ? cwd : undefined
}

/**
 * Read `remote.origin.url` and parse the repository name out of it.
 *
 * `origin` is the remote every clone has; a directory whose remote is named
 * otherwise still falls back to its own name, which is the same bank it would
 * have had from the directory alone.
 * @param cwd - a directory to ask git about.
 * @returns the repository name, or undefined when git cannot name one.
 */
async function readGitRepo(cwd: string): Promise<string | undefined> {
  const url = await new Promise<string | undefined>((resolve) => {
    try {
      execFile(
        'git',
        ['config', '--get', 'remote.origin.url'],
        { cwd, timeout: GIT_TIMEOUT_MS, windowsHide: true },
        (error, stdout) => { resolve(error === null ? stdout : undefined) },
      )
    } catch {
      // A missing `git` binary throws synchronously out of execFile itself.
      resolve(undefined)
    }
  })
  return parseGitRepo(url)
}

/**
 * The repository a working directory belongs to, cached per directory.
 * @param cwd - the session's working directory, possibly absent.
 * @returns the repository name, or undefined when none can be read.
 */
export async function gitRepoAt(cwd: string | undefined): Promise<string | undefined> {
  if (cwd === undefined) return undefined
  const cached = remotes.get(cwd)
  const now = Date.now()
  if (cached !== undefined && now - cached.readAt < REMOTE_TTL_MS) return cached.repo
  const repo = await readGitRepo(cwd)
  remotes.set(cwd, { repo, readAt: now })
  return repo
}

/**
 * Forget every cached remote answer, so the next resolution reads git again.
 * Used by the tests; the TTL handles it in a running plugin.
 */
export function clearRepoCache(): void {
  remotes.clear()
}