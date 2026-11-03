/**
 * The opencode adapter, driven against stand-in cores.
 *
 * The `config` hook teaches the host about the survey agent, so the recipe in core/survey.ts
 * (`opencode run --agent wenyuan-survey`) needs nothing in the user's opencode.json. See
 * SURVEY_AGENT in core/survey.ts for why the built-in `plan` agent can't do it. The hook wiring
 * tests pin the seams a live host drives: recall per prompt, injection per system transform, and
 * the session.idle write-back.
 */
import { describe, expect, it, vi } from "vitest";
import { opencodeAdapter } from "../../../src/harness/opencode";
import type { RuntimeCore } from "../../../src/core/runtime";
import { SURVEY_AGENT, SURVEY_AGENT_CONFIG } from "../../../src/core/survey";

/** Only the members createRuntime touches when it builds the hook object. */
function fakeCore(extra: Partial<Record<string, unknown>> = {}) {
  return {
    harness: "opencode",
    toolSpecs: () => [],
    onPrompt: vi.fn(async () => {}),
    getInjection: vi.fn(() => undefined),
    onTranscript: vi.fn(async () => {}),
    onSessionIdle: vi.fn(async () => {}),
    writeBackEnabled: true,
    ...extra,
  } as unknown as RuntimeCore & Record<string, ReturnType<typeof vi.fn> & boolean>;
}

type ConfigHook = (cfg: { agent?: Record<string, unknown> }) => Promise<void>;

function configHook(): ConfigHook {
  const runtime = opencodeAdapter.createRuntime(fakeCore()) as { config?: ConfigHook };
  if (!runtime.config) throw new Error("adapter exposes no config hook");
  return runtime.config;
}

describe("opencode config hook", () => {
  it("defines the survey agent on a config that has no agents at all", async () => {
    const cfg: { agent?: Record<string, unknown> } = {};
    await configHook()(cfg);
    expect(cfg.agent?.[SURVEY_AGENT]).toEqual(SURVEY_AGENT_CONFIG);
  });

  it("leaves the user's other agents alone", async () => {
    const mine = { description: "mine" };
    const cfg = { agent: { reviewer: mine } as Record<string, unknown> };
    await configHook()(cfg);
    expect(cfg.agent?.reviewer).toBe(mine);
    expect(cfg.agent?.[SURVEY_AGENT]).toEqual(SURVEY_AGENT_CONFIG);
  });

  it("does NOT overwrite a wenyuan-survey agent the user defined themselves", async () => {
    // Their file is the last word: someone who redefines this name has a reason, and silently
    // replacing it would be a plugin overriding local config.
    const theirs = { description: "customised", mode: "primary" };
    const cfg = { agent: { [SURVEY_AGENT]: theirs } as Record<string, unknown> };
    await configHook()(cfg);
    expect(cfg.agent?.[SURVEY_AGENT]).toBe(theirs);
  });
});

describe("opencode runtime hooks", () => {
  it("registers every tool spec natively and joins the MCP text result", async () => {
    const core = fakeCore({
      toolSpecs: () => [
        {
          name: "wenyuan_search_knowledge_pages",
          description: "search",
          inputSchema: { type: "object", properties: {} },
          handler: async () => ({ content: [{ type: "text", text: "a\nb" }] }),
        },
      ],
    });
    const runtime = opencodeAdapter.createRuntime(core) as {
      tool: Record<string, { description: string; execute(a: Record<string, unknown>): Promise<string> }>;
    };
    expect(Object.keys(runtime.tool)).toEqual(["wenyuan_search_knowledge_pages"]);
    const tool = runtime.tool["wenyuan_search_knowledge_pages"];
    if (!tool) throw new Error("tool not registered");
    expect(tool.description).toBe("search");
    await expect(tool.execute({})).resolves.toBe("a\nb");
  });

  it("recalls on the prompt text and pushes the injection into the system prompt", async () => {
    const core = fakeCore({ getInjection: () => "<wenyuan_memories>remember</wenyuan_memories>" });
    const runtime = opencodeAdapter.createRuntime(core) as Record<string, (...a: never[]) => unknown>;
    await runtime["chat.message"]!(
      { sessionID: "ses_1" } as never,
      { parts: [{ type: "text", text: "plan the change" }] } as never
    );
    expect(core.onPrompt).toHaveBeenCalledWith("ses_1", "plan the change");

    const system: string[] = ["you are opencode"];
    await runtime["experimental.chat.system.transform"]!({ sessionID: "ses_1" } as never, {
      system,
    } as never);
    expect(system).toEqual(["you are opencode", "<wenyuan_memories>remember</wenyuan_memories>"]);
  });

  it("retains on session.idle and ignores every other event", async () => {
    const core = fakeCore();
    const runtime = opencodeAdapter.createRuntime(core) as {
      event: (input: { event?: { type?: string; properties?: { sessionID?: string } } }) => Promise<void>;
    };
    await runtime.event({ event: { type: "other.event", properties: { sessionID: "s" } } });
    expect(core.onSessionIdle).not.toHaveBeenCalled();
    await runtime.event({ event: { type: "session.idle" } });
    expect(core.onSessionIdle).not.toHaveBeenCalled(); // no session id: skipped
    await runtime.event({ event: { type: "session.idle", properties: { sessionID: "ses_1" } } });
    expect(core.onSessionIdle).toHaveBeenCalledWith("ses_1");
  });

  it("skips the mid-session transcript cadence when write-back is off", async () => {
    const core = fakeCore({ writeBackEnabled: false });
    const runtime = opencodeAdapter.createRuntime(core) as Record<string, (...a: never[]) => unknown>;
    await runtime["experimental.chat.messages.transform"]!({} as never, {
      messages: [{ type: "user", parts: [{ type: "text", text: "hi" }] }],
    } as never);
    expect(core.onTranscript).not.toHaveBeenCalled();
  });
});
