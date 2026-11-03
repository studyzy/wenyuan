import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { syncGit } from "../../../src/core/sync";
import type { MemoryClient } from "../../../src/core/client";

const repos: string[] = [];

function repoWithCommits(n: number): string {
  const repo = mkdtempSync(join(tmpdir(), "wy-sync-"));
  repos.push(repo);
  execFileSync("git", ["-C", repo, "init", "-q", "-b", "main"]);
  execFileSync("git", ["-C", repo, "config", "user.email", "test@example.com"]);
  execFileSync("git", ["-C", repo, "config", "user.name", "Test User"]);
  for (let i = 0; i < n; i++) {
    writeCommit(repo, `commit ${i}`);
  }
  return repo;
}

function writeCommit(repo: string, message: string): string {
  execFileSync("git", ["-C", repo, "commit", "-q", "--allow-empty", "-m", message]);
  return String(execFileSync("git", ["-C", repo, "rev-parse", "HEAD"], { encoding: "utf8" }).trim());
}

function clientWith(seen: Set<string>, failures = 0) {
  const retained: string[] = [];
  let remaining = failures;
  const client = {
    listDocumentIds: vi.fn(async () => new Set(seen)),
    retain: vi.fn(async (_text: string, _ctx: string, docId: string) => {
      if (remaining > 0) {
        remaining--;
        throw new Error("enqueue refused");
      }
      retained.push(docId);
    }),
  } as unknown as MemoryClient;
  return { client, retained };
}

afterEach(() => {
  while (repos.length) rmSync(repos.pop()!, { recursive: true, force: true });
});

describe("syncGit", () => {
  it("an empty repo (or non-repo) syncs nothing and reports inSync", async () => {
    const { client } = clientWith(new Set());
    const result = await syncGit(client, join(tmpdir(), "wy-sync-missing-dir"));
    expect(result).toEqual({ ref: null, total: 0, ingested: 0, failures: 0, inSync: true });
    expect(client.listDocumentIds).not.toHaveBeenCalled();
  });

  it("falls back to HEAD when the preferred ref does not resolve", async () => {
    const repo = repoWithCommits(2);
    const { client, retained } = clientWith(new Set());
    const result = await syncGit(client, repo, { ref: "origin/main" });
    expect(result.ref).toBe("HEAD");
    expect(result.total).toBe(2);
    expect(result.ingested).toBe(2);
    expect(retained.sort()).toEqual(
      (await shas(repo)).map((sha) => `git:${sha}`).sort()
    );
  });

  it("retains only the commits the bank is missing, and is in sync when none are", async () => {
    const repo = repoWithCommits(3);
    const shas = await allShas(repo);
    // The bank already holds every commit but the newest.
    const seen = new Set(shas.slice(1).map((sha) => `git:${sha}`));
    const { client, retained } = clientWith(seen);
    const result = await syncGit(client, repo, { ref: "HEAD" });
    expect(result.inSync).toBe(false);
    expect(result.ingested).toBe(1);
    expect(retained).toEqual([`git:${shas[0]}`]);

    // A second run over the same state is a no-op: the set-based check is the idempotence.
    const { client: again } = clientWith(new Set([...seen, `git:${shas[0]}`]));
    const second = await syncGit(again, repo, { ref: "HEAD" });
    expect(second).toMatchObject({ total: 3, ingested: 0, failures: 0, inSync: true });
  });

  it("is correct across a rebase: whatever the ref now names gets ingested, stale ids simply stay", async () => {
    const repo = repoWithCommits(2);
    const oldShas = await allShas(repo);
    // Rewrite history: the old commits vanish, new ones with the same messages appear.
    execFileSync("git", ["-C", repo, "checkout", "-q", "--orphan", "rebased"]);
    execFileSync("git", ["-C", repo, "commit", "-q", "--allow-empty", "-m", "rebased root"]);
    execFileSync("git", ["-C", repo, "commit", "-q", "--allow-empty", "-m", "rebased second"]);
    execFileSync("git", ["-C", repo, "branch", "-q", "-D", "main"]);
    execFileSync("git", ["-C", repo, "branch", "-q", "-m", "main"]);
    const newShas = await allShas(repo);
    expect(newShas).not.toEqual(oldShas);

    const seen = new Set(oldShas.map((sha) => `git:${sha}`));
    const { client, retained } = clientWith(seen);
    const result = await syncGit(client, repo, { ref: "HEAD" });
    expect(result.ingested).toBe(2);
    expect(retained.sort()).toEqual(newShas.map((sha) => `git:${sha}`).sort());
  });

  it("counts enqueues that failed instead of failing the sync", async () => {
    const repo = repoWithCommits(2);
    const { client, retained } = clientWith(new Set(), 1);
    const result = await syncGit(client, repo, { ref: "HEAD" });
    expect(result.failures).toBe(1);
    expect(result.ingested).toBe(1);
    expect(result.inSync).toBe(false);
    expect(retained).toHaveLength(1);
  });
});

async function shas(repo: string): Promise<string[]> {
  return String(
    execFileSync("git", ["-C", repo, "rev-list", "--reverse", "HEAD"], { encoding: "utf8" })
  )
    .trim()
    .split("\n")
    .filter(Boolean);
}

async function allShas(repo: string): Promise<string[]> {
  return shas(repo);
}
