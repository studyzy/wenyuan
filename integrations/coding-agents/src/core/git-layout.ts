/**
 * Where does this directory's git repository keep its common directory? — answered by READING the
 * repository layout off disk, never by spawning `git`.
 *
 * This probe is what bank identity is built on, and bank identity must not degrade silently: a
 * wrong id is not a degraded session, it is a *different* memory. A spawn-based probe maps every
 * failure (timeout under load, a failed spawn itself) to "not a repository", which is the answer
 * that sends a linked worktree to the basename fallback and forks it into its own bank forever.
 * Reading the layout removes the failure mode instead of widening its budget: no subprocess, no
 * timeout, no spawn. It is also what git itself does — `.git` is either the git directory or a
 * one-line pointer to it, and a linked worktree's git directory carries a `commondir` file naming
 * the repository it belongs to:
 *
 *   <main>/.git/            ordinary checkout                  -> common dir <main>/.git
 *   <wt>/.git               "gitdir: <main>/.git/worktrees/<n>" -> commondir "../.." -> <main>/.git
 *   <hub>/.git              "gitdir: ./.bare"                   -> common dir <hub>/.bare
 *   <repo>.git/             bare clone (HEAD + objects + refs)  -> common dir <repo>.git
 *
 * A pure-JS git library was considered and rejected: they model objects and refs, not worktree
 * discovery — and this file is ~100 lines of fs reads against a format git itself guarantees, in
 * a package that is bundled into every hook process.
 *
 * Environment (GIT_DIR, GIT_COMMON_DIR) is deliberately ignored: a hook can inherit a stale one
 * from whatever spawned it, and discovery from the directory is the question actually being asked.
 */
import { lstatSync, readFileSync, realpathSync } from "node:fs";
import { dirname, isAbsolute, join, resolve } from "node:path";

export type GitLayout =
  /** The directory is inside a repository whose common directory is `commonDir`. */
  | { status: "resolved"; commonDir: string; bare: boolean }
  /** The directory is definitively NOT inside a repository — the walk reached the filesystem root. */
  | { status: "absent" }
  /** The probe could not complete. NOT the same as absent: callers must not guess an identity. */
  | { status: "failed"; reason: string };

/** Errors worth another attempt: resource pressure, not an answer about the filesystem. */
const TRANSIENT = new Set(["EAGAIN", "EMFILE", "ENFILE", "EBUSY", "EINTR", "EIO", "ETIMEDOUT"]);

const ATTEMPTS = 3;
const BACKOFF_MS = 20;

function errorCode(error: unknown): string {
  return (error as NodeJS.ErrnoException | undefined)?.code ?? "";
}

/** Synchronous backoff — the whole resolution path is sync (hooks call it before any await). */
function sleepSync(ms: number): void {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
}

class TransientProbeError extends Error {}

/** `lstat` reduced to the three answers the walk cares about; transient failures propagate. */
function entryKind(path: string): "dir" | "file" | "none" {
  try {
    const st = lstatSync(path);
    return st.isDirectory() ? "dir" : "file";
  } catch (error) {
    if (TRANSIENT.has(errorCode(error))) throw new TransientProbeError(errorCode(error));
    return "none"; // ENOENT, ENOTDIR, and unreadable paths: nothing discoverable here
  }
}

function readTextOrNull(path: string): string | null {
  try {
    return readFileSync(path, "utf8");
  } catch (error) {
    if (TRANSIENT.has(errorCode(error))) throw new TransientProbeError(errorCode(error));
    return null;
  }
}

/** `gitdir: <path>` from a `.git` FILE (linked worktree, submodule, bare hub), resolved against
 *  the directory holding it — the pointer is allowed to be relative (`gitdir: ./.bare`). */
function gitDirFromPointer(text: string, holder: string): string | null {
  const match = /^\s*gitdir:\s*(.+?)\s*$/m.exec(text);
  const target = match?.[1];
  if (!target) return null;
  return isAbsolute(target) ? target : resolve(holder, target);
}

/** The repository-wide directory for a git directory: a linked worktree's own git directory names
 *  it in `commondir`, everything else IS it. This single hop is what makes worktrees share a bank. */
function commonDirOf(gitDir: string): string {
  const pointer =
    entryKind(join(gitDir, "commondir")) === "file"
      ? readTextOrNull(join(gitDir, "commondir"))?.trim()
      : null;
  if (!pointer) return gitDir;
  return isAbsolute(pointer) ? pointer : resolve(gitDir, pointer);
}

/** `core.bare = true` in the repository's own config. */
function isBare(commonDir: string): boolean {
  const config = readTextOrNull(join(commonDir, "config"));
  return config !== null && /^\s*bare\s*=\s*true\s*$/im.test(config);
}

/** A directory that IS a repository (no worktree, no `.git`): `git init --bare` output. */
function looksLikeBareRepository(directory: string): boolean {
  return (
    entryKind(join(directory, "HEAD")) === "file" &&
    entryKind(join(directory, "objects")) === "dir" &&
    entryKind(join(directory, "refs")) === "dir"
  );
}

function canonical(path: string): string {
  try {
    return realpathSync(path);
  } catch {
    return path; // a path we cannot canonicalise is still the right answer, just not resolved
  }
}

/** A resolved answer, or null when the layout points at a directory that is no longer there — a
 *  worktree whose administrative directory has been pruned. Naming THAT path would hand the bank
 *  the worktree's own name, the exact outcome this file exists to prevent, so the walk goes on. */
function resolved(commonDir: string): GitLayout | null {
  if (entryKind(commonDir) !== "dir") return null;
  const canonicalDir = canonical(commonDir);
  return { status: "resolved", commonDir: canonicalDir, bare: isBare(canonicalDir) };
}

function probeOnce(directory: string): GitLayout {
  let current = resolve(directory);
  for (;;) {
    const dotGit = join(current, ".git");
    const kind = entryKind(dotGit);
    if (kind === "dir") {
      const layout = resolved(commonDirOf(dotGit));
      if (layout) return layout;
    } else if (kind === "file") {
      const text = readTextOrNull(dotGit);
      const gitDir = text ? gitDirFromPointer(text, current) : null;
      const layout = gitDir ? resolved(commonDirOf(gitDir)) : null;
      if (layout) return layout;
    }
    if (looksLikeBareRepository(current)) {
      const layout = resolved(current);
      if (layout) return layout;
    }
    const parent = dirname(current);
    if (parent === current) return { status: "absent" };
    current = parent;
  }
}

/**
 * The common directory for `directory`, distinguishing "not a repository" from "could not tell".
 *
 * Retries a transient failure (resource pressure) before giving up, because giving up here is
 * what costs a session its memory.
 */
export function probeGitLayout(directory: string): GitLayout {
  if (!directory) return { status: "absent" };
  let last = "";
  for (let attempt = 0; attempt < ATTEMPTS; attempt++) {
    if (attempt) sleepSync(BACKOFF_MS * attempt);
    try {
      return probeOnce(directory);
    } catch (error) {
      last = error instanceof TransientProbeError ? error.message : errorCode(error) || "unknown";
    }
  }
  return { status: "failed", reason: last };
}
