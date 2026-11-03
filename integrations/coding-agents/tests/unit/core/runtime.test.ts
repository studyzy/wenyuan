import { existsSync, mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { resolveConfig } from "../../../src/core/config";
import type { MemoryClient } from "../../../src/core/client";
import { RuntimeCore } from "../../../src/core/runtime";

// Whether a bank is new enough to defer is session-start's call (session-start.test.ts); here only
// the hand-off of that decision to the first session is under test, so it can be forced.
const forceDefer = vi.hoisted(() => ({ on: false }));
vi.mock("../../../src/core/session-start", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../../src/core/session-start")>();
  return {
    ...actual,
    buildSessionStartContext: async (...a: Parameters<typeof actual.buildSessionStartContext>) => {
      const out = await actual.buildSessionStartContext(...a);
      return forceDefer.on ? { ...out, deferInitialReflect: true } : out;
    },
  };
});

describe("RuntimeCore", () => {
  it("uses the shared prompt lifecycle and consumes the new-bank reflect deferral once", async () => {
    const client = {
      listDocumentIds: vi.fn(async () => new Set(["git:existing"])),
      listPages: vi.fn(async () => ({ items: [] })),
      reflect: vi.fn(async () => "shared reflect"),
    } as unknown as MemoryClient;
    const runtime = new RuntimeCore(client, "bank-1", resolveConfig({}));

    forceDefer.on = true;
    try {
      await runtime.seedIfCold("/definitely-not-a-git-repository");
    } finally {
      forceDefer.on = false;
    }
    await runtime.onPrompt("runtime-shared-lifecycle", "first prompt");
    expect(client.reflect).not.toHaveBeenCalled();

    await runtime.onPrompt("runtime-shared-lifecycle", "second prompt");
    expect(client.reflect).toHaveBeenCalledTimes(1);
    expect(runtime.getInjection("runtime-shared-lifecycle")).toContain("shared reflect");
  });

  it("the knowledge roster tracks the pages a SESSION sees, not the ones plugin load saw", async () => {
    // These hosts outlive every session. A preamble frozen at load told each new session "No
    // knowledge pages yet" for the process's whole life, while the same turn's memory block listed
    // pages by id — the agent was told both, and believed the stale half.
    let pages: { id: string; name: string }[] = [];
    const client = {
      listDocumentIds: vi.fn(async () => new Set(["git:existing"])),
      listPages: vi.fn(async () => ({ items: pages })),
      reflect: vi.fn(async () => ""),
    } as unknown as MemoryClient;
    const runtime = new RuntimeCore(client, "bank-1", resolveConfig({}));

    await runtime.seedIfCold("/definitely-not-a-git-repository"); // cold: zero pages
    await runtime.onPrompt("session-cold", "first prompt");
    expect(runtime.getInjection("session-cold")).toContain("No knowledge pages yet");

    pages = [
      { id: "kp-1", name: "Component map" },
      { id: "kp-2", name: "Key decisions and rationale" },
    ];
    await runtime.onPrompt("session-warm", "first prompt");
    const warm = runtime.getInjection("session-warm") ?? "";
    expect(warm).toContain("2 knowledge pages cover");
    expect(warm).not.toContain("No knowledge pages yet");
  });
});

/**
 * The message transform fires while the host BUILDS a request, so the transcript it passes never
 * contains the reply that request produces. session-idle is the only moment a completed exchange is
 * readable — without it write-back lagged a turn and a session's final exchange was lost entirely.
 */
describe("RuntimeCore session-idle write-back", () => {
  const turn = (role: string, content: string) => ({ role, content }) as never;

  /** A client that records what retain() was given. */
  function makeClient() {
    const retained: { turns: unknown; docId: string }[] = [];
    const client = {
      retain: vi.fn(async (text: string, _ctx: string, docId: string) => {
        retained.push({ turns: text, docId });
      }),
    } as unknown as MemoryClient;
    return { client, retained };
  }

  it("retains the completed exchange on idle — the reply the turn-driven path never sees", async () => {
    const { client, retained } = makeClient();
    const runtime = new RuntimeCore(client, "bank-1", resolveConfig({}));

    // The turn-driven path sees only what existed BEFORE the reply.
    await runtime.onTranscript("s1", [turn("user", "how do we round?")], false);
    await new Promise((r) => setTimeout(r, 0)); // retain is fire-and-forget
    expect(retained).toHaveLength(1);
    expect(String(retained[0]?.turns)).not.toContain("we round half up");

    // Idle refetches, so the assistant's answer finally lands.
    runtime.setTranscriptSource(async () => [
      turn("user", "how do we round?"),
      turn("assistant", "we round half up"),
    ]);
    await runtime.onSessionIdle("s1");
    await new Promise((r) => setTimeout(r, 0)); // retain is fire-and-forget

    expect(retained).toHaveLength(2);
    expect(String(retained[1]?.turns)).toContain("we round half up");
    expect(retained[1]?.docId).toBe("conversation:s1");
  });

  it("writes back every turn — no client-side cadence to strand a short session", async () => {
    // There used to be a retainEveryTurns threshold here, which suppressed a session shorter than
    // it entirely. Batching happens server-side instead, so every turn is submitted immediately and
    // nothing is held in a process the host can close without warning.
    const { client, retained } = makeClient();
    const runtime = new RuntimeCore(client, "bank-1", resolveConfig({}));

    await runtime.onTranscript("s2", [turn("user", "only turn")], false);
    await new Promise((r) => setTimeout(r, 0));
    expect(retained).toHaveLength(1);

    runtime.setTranscriptSource(async () => [turn("user", "only turn"), turn("assistant", "done")]);
    await runtime.onSessionIdle("s2");
    await new Promise((r) => setTimeout(r, 0));
    expect(retained).toHaveLength(2);
  });

  it("does not re-retain when idle fires again with no new turns", async () => {
    const { client, retained } = makeClient();
    const runtime = new RuntimeCore(client, "bank-1", resolveConfig({}));
    runtime.setTranscriptSource(async () => [turn("user", "hi"), turn("assistant", "hello")]);

    await runtime.onSessionIdle("s3");
    await runtime.onSessionIdle("s3");
    await new Promise((r) => setTimeout(r, 0));
    expect(retained).toHaveLength(1);
  });

  it("records memory usage for a turn only once its reply is in", async () => {
    const usageFile = join(mkdtempSync(join(tmpdir(), "wy-rt-usage-")), "usage.jsonl");
    vi.stubEnv("WENYUAN_AGENTS_USAGE_FILE", usageFile);
    try {
      const { client } = makeClient();
      const runtime = new RuntimeCore(client, "bank-1", resolveConfig({}), "kilo");

      // Built before the reply: recording turn 1 now would log it with no calls, for good.
      await runtime.onTranscript("s6", [turn("user", "how do we round?")], false);
      expect(existsSync(usageFile)).toBe(false);

      runtime.setTranscriptSource(async () => [
        turn("user", "how do we round?"),
        turn("action", "wenyuan_search_knowledge_pages rounding"),
        turn("assistant", "🧠 From WenYuan memory — half up"),
      ]);
      await runtime.onSessionIdle("s6");
      const recorded = readFileSync(usageFile, "utf8")
        .trim()
        .split("\n")
        .map((l) => JSON.parse(l));
      expect(recorded).toMatchObject([
        {
          harness: "kilo",
          session: "s6",
          bank: "bank-1",
          turn: 1,
          calls: ["wenyuan_search_knowledge_pages"],
          credited: true,
        },
      ]);
    } finally {
      vi.unstubAllEnvs();
    }
  });

  it("records usage with retainSessions off, and still retains nothing", async () => {
    const usageFile = join(mkdtempSync(join(tmpdir(), "wy-rt-usage-")), "usage.jsonl");
    vi.stubEnv("WENYUAN_AGENTS_USAGE_FILE", usageFile);
    try {
      const { client, retained } = makeClient();
      const runtime = new RuntimeCore(client, "bank-1", resolveConfig({ retainSessions: false }));
      const turns = [turn("user", "how do we round?"), turn("assistant", "half up")];
      await runtime.onTranscript("s7", turns, true);
      runtime.setTranscriptSource(async () => [
        ...turns,
        turn("user", "and ties?"),
        turn("assistant", "even"),
      ]);
      await runtime.onSessionIdle("s7");
      await new Promise((r) => setTimeout(r, 0));
      expect(retained).toHaveLength(0);
      const recorded = readFileSync(usageFile, "utf8").trim().split("\n");
      expect(recorded.map((l) => JSON.parse(l).turn)).toEqual([1, 2]);
    } finally {
      vi.unstubAllEnvs();
    }
  });

  it("no-ops when the host cannot refetch, rather than retaining a stale transcript", async () => {
    const { client, retained } = makeClient();
    const runtime = new RuntimeCore(client, "bank-1", resolveConfig({}));
    await runtime.onSessionIdle("s4"); // no transcript source wired
    await new Promise((r) => setTimeout(r, 0));
    expect(retained).toHaveLength(0);
  });

  it("survives a failing refetch without throwing into the host", async () => {
    const { client, retained } = makeClient();
    const runtime = new RuntimeCore(client, "bank-1", resolveConfig({}));
    runtime.setTranscriptSource(async () => {
      throw new Error("session gone");
    });
    await expect(runtime.onSessionIdle("s5")).resolves.toBeUndefined();
    expect(retained).toHaveLength(0);
  });
});

/**
 * The plugin harnesses register the tools through toolSpecs() rather than the MCP server, so they
 * hit the same aborted-deadline bug: without these forwarded, every wenyuan_reflect on those hosts
 * aborts at the client's hardcoded 120s.
 */
describe("RuntimeCore reflect tool settings", () => {
  it("forwards the resolved reflect timeout and budget into wenyuan_reflect", async () => {
    const reflect = vi.fn(async () => "answer");
    const client = { reflect } as unknown as MemoryClient;
    const core = new RuntimeCore(
      client,
      "bank-1",
      resolveConfig({ reflectToolTimeoutMs: 660_000, reflectBudget: "mid" })
    );

    const tool = core.toolSpecs().find((spec) => spec.name === "wenyuan_reflect");
    expect(tool).toBeTruthy();
    await tool?.handler({ query: "why?" });
    expect(reflect).toHaveBeenCalledWith("why?", { budget: "mid", timeoutMs: 660_000 });
  });

  it("wires wenyuan_sync_status through the shared status closure", async () => {
    const client = {
      listDocumentIds: vi.fn(async () => new Set<string>()),
      listPages: vi.fn(async () => ({ items: [] })),
      activeOperations: vi.fn(async () => 0),
    } as unknown as MemoryClient;
    const core = new RuntimeCore(client, "bank-1", resolveConfig({}), "dsh", "/repos/one");
    const tool = core.toolSpecs().find((spec) => spec.name === "wenyuan_sync_status");
    expect(tool).toBeTruthy();
    const result = await tool?.handler({});
    const payload = JSON.parse(result?.content[0]?.text ?? "{}") as Record<string, unknown>;
    // The counters resolved against THIS bank and workspace, not the process cwd.
    expect(payload.bank).toBe("bank-1");
    expect(client.listDocumentIds).toHaveBeenCalled();
  });
});

/**
 * dsh serves several repositories from ONE process, launched in a directory that is routinely not
 * the session's — so it constructs a core per workspace and passes that root. Every tool that
 * answers "what does this repo's memory look like" has to be bound to it: otherwise
 * `wenyuan_sync_status` runs its git checks in the launch directory and looks for THAT repo's
 * `gitlog:<name>` document in the SESSION's bank, reporting a seeded repo as unsynced.
 */
describe("RuntimeCore tool workspace binding", () => {
  const client = {
    listDocumentIds: vi.fn(async () => new Set<string>()),
    listPages: vi.fn(async () => ({ items: [] })),
    activeOperations: vi.fn(async () => 0),
  } as unknown as MemoryClient;

  const workspaceOf = async (core: RuntimeCore): Promise<string> => {
    const diagnose = core.toolSpecs().find((spec) => spec.name === "wenyuan_diagnose");
    if (!diagnose) throw new Error("wenyuan_diagnose not registered");
    const result = await diagnose.handler({});
    return (JSON.parse(result.content[0]?.text ?? "{}") as { workspace: string }).workspace;
  };

  it("binds the tools to the workspace the host opened, not the process cwd", async () => {
    const core = new RuntimeCore(client, "bank-1", resolveConfig({}), "dsh", "/repos/session-one");
    expect(await workspaceOf(core)).toBe("/repos/session-one");
    expect(await workspaceOf(core)).not.toBe(process.cwd());
  });

  it("falls back to the process cwd for hosts whose process IS the project", async () => {
    const core = new RuntimeCore(client, "bank-1", resolveConfig({}));
    expect(await workspaceOf(core)).toBe(process.cwd());
  });
});

/** idle/agent-end write-backs are fire-and-forget; a host that exits right after one dropped it. */
describe("RuntimeCore.flush", () => {
  it("waits for an in-flight write-back, and gives up after its bound", async () => {
    let release: (() => void) | undefined;
    const client = {
      retain: vi.fn(() => new Promise<void>((r) => (release = r))),
    } as unknown as MemoryClient;
    const runtime = new RuntimeCore(client, "bank-1", resolveConfig({}));
    await runtime.onTranscript("s-flush", [{ role: "user", content: "hi" }], true);

    const t0 = Date.now();
    await runtime.flush(50); // the server never answers: flush must still return
    expect(Date.now() - t0).toBeGreaterThanOrEqual(45);

    let flushed = false;
    const done = runtime.flush(5_000).then(() => (flushed = true));
    await new Promise((r) => setTimeout(r, 10));
    expect(flushed).toBe(false);
    release?.();
    await done;
    expect(client.retain).toHaveBeenCalledOnce();
  });
});

/** The hooks guard silences every entry point, not just the hook binaries. */
describe("RuntimeCore anti-recursion guard", () => {
  const ORIGINAL = process.env.WENYUAN_DISABLE_HOOKS;
  afterEach(() => {
    if (ORIGINAL === undefined) delete process.env.WENYUAN_DISABLE_HOOKS;
    else process.env.WENYUAN_DISABLE_HOOKS = ORIGINAL;
  });

  it("WENYUAN_DISABLE_HOOKS set -> seedIfCold, onPrompt, onTranscript and onSessionIdle all no-op", async () => {
    process.env.WENYUAN_DISABLE_HOOKS = "1";
    const client = {
      listDocumentIds: vi.fn(async () => new Set<string>()),
      listPages: vi.fn(async () => ({ items: [] })),
      retain: vi.fn(async () => {}),
    } as unknown as MemoryClient;
    const runtime = new RuntimeCore(client, "bank-1", resolveConfig({}));
    await runtime.seedIfCold("/definitely-not-a-git-repository");
    await runtime.onPrompt("s-guard", "prompt");
    await runtime.onTranscript("s-guard", [{ role: "user", content: "hi" }] as never, true);
    await runtime.onSessionIdle("s-guard");
    expect(client.listDocumentIds).not.toHaveBeenCalled();
    expect(client.retain).not.toHaveBeenCalled();
    // no injection block was built: the pre-turn state is the empty fallback
    expect(runtime.getInjection("s-guard")).toBe("");
  });
});
