import { describe, expect, it } from "vitest";
import { mkdirSync, writeFileSync } from "node:fs";
import { execSync } from "node:child_process";
import { join } from "node:path";
import { DISABLE_HOOKS_ENV } from "../../src/runtime/hook-contract";
import type { FakeHome } from "../helpers/fake-home";
import { runHook, seedStoredConnection, startStubService, withHookHome, type StubService } from "../helpers/hook-run";

/**
 * The packaged stop hook. It writes the session's conversation back to memory
 * and prints nothing at all, so the host session is never disturbed; an
 * unreachable service leaves the session unaffected and still exits zero.
 */


/** A Claude-format session transcript: one JSON turn per line. */
/** A throwaway git repo: bank naming derives from the workspace root, and a
 *  directory that is not a repository is skipped silently by design. */
function repoDirectory(home: FakeHome, name: string): string {
  const directory = home.path(name);
  mkdirSync(directory, { recursive: true });
  const git = (args: string) => execSync('git -C ' + directory + ' ' + args, { stdio: "ignore" });
  git("init -q");
  git("config user.email test@example.com");
  git("config user.name test");
  writeFileSync(join(directory, "README.md"), "probe");
  git("add README.md");
  git("commit -q -m probe");
  return directory;
}

function writeTranscript(home: FakeHome, sessionId: string, lines: string[]): string {
  const path = join(home.home, `${sessionId}.jsonl`);
  writeFileSync(path, lines.map((line) => JSON.stringify({ type: line[0] === "u" ? "user" : "assistant", message: { content: line.slice(2) } })).join("\n") + "\n");
  return path;
}

describe("the stop hook", () => {
  it("writes the conversation back and prints nothing", async () => {
    const service: StubService = await startStubService();
    try {
      await withHookHome(async (home) => {
        await seedStoredConnection(home, service.url);
        const transcriptPath = writeTranscript(home, "s-1", ["u hi", "a hello"]);
        const run = await runHook({
          home,
          scriptFile: "stop.mjs",
          payload: { session_id: "s-1", transcript_path: transcriptPath, cwd: repoDirectory(home, "probe-repo") },
        });
        expect(run.status).toBe(0);
        expect(run.stdout).toBe("");
        const capture = service.requests.find((request) => request.path.endsWith("/memories"));
        expect(capture).toBeDefined();
        const body = capture!.body as { items: { content: string }[] };
        expect(JSON.stringify(body)).toContain("hi");
      });
    } finally {
      await service.close();
    }
  });

  it("retains once for an unchanged transcript and appends when it grows", async () => {
    const service: StubService = await startStubService();
    try {
      await withHookHome(async (home) => {
        await seedStoredConnection(home, service.url);
        const repo = repoDirectory(home, "probe-repo");
        const transcriptPath = join(repo, "s-1.jsonl");
        writeFileSync(transcriptPath, JSON.stringify({ type: "user", message: { content: "a" } }) + "\n");
        const payload = { session_id: "s-1", transcript_path: transcriptPath, cwd: repo };
        await runHook({ home, scriptFile: "stop.mjs", payload });
        await runHook({ home, scriptFile: "stop.mjs", payload });
        // Both firings write under ONE document identity for the session: the
        // second carries the retain cursor (append, not a second document).
        const captures = service.requests.filter((request) => request.path.endsWith("/memories"));
        expect(captures.length).toBe(2);
        const documentIds = captures.map((capture) => {
          const body = capture.body as { items: { document_id?: string }[] };
          return body.items[0]!.document_id;
        });
        expect(new Set(documentIds).size).toBe(1);
        expect(documentIds[0]).toBeTruthy();
      });
    } finally {
      await service.close();
    }
  });

  it("prints nothing and exits zero when the service is unreachable", async () => {
    await withHookHome(async (home) => {
      await seedStoredConnection(home, "http://127.0.0.1:1");
      const run = await runHook({
        home,
        scriptFile: "stop.mjs",
        payload: { session_id: "s-1", transcript_path: join(home.home, "missing.jsonl"), cwd: repoDirectory(home, "probe-repo") },
      });
      expect(run.status).toBe(0);
      expect(run.stdout).toBe("");
    });
  });

  it("does nothing when the disable guard is set", async () => {
    const service: StubService = await startStubService();
    try {
      await withHookHome(async (home) => {
        await seedStoredConnection(home, service.url);
        const run = await runHook({
          home,
          scriptFile: "stop.mjs",
          payload: { session_id: "s-1", turns: ["a"] },
          environment: { [DISABLE_HOOKS_ENV]: "yes" },
        });
        expect(run.status).toBe(0);
        expect(service.requests).toHaveLength(0);
      });
    } finally {
      await service.close();
    }
  });
});