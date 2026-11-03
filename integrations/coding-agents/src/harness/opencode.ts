/**
 * opencode harness adapter — full v2 parity with the hook harnesses.
 *
 * Maps opencode's persistent-plugin hooks onto the shared RuntimeCore, and reads opencode sessions
 * for backfill. opencode is the cleanest platform of the lot: a real per-turn event, a working
 * system-prompt injection channel, transcript access, and NATIVE tool registration — so the whole
 * v2 surface (per-turn recall + attribution/user-feedback injection, the wenyuan_* knowledge
 * tools, cold-check auto-seed, rich write-back) rides these five hooks with no MCP server needed.
 *
 * Every host shape is STRUCTURALLY typed — no SDK import, nothing for a package manager to resolve
 * inside a profile.
 */
import type { RuntimeCore } from "../core/runtime";
import type { HarnessAdapter } from "../core/types";
import { diag } from "../core/diag";
import type { ToolInputSchema, ToolSpec } from "../core/knowledge-tools";
import {
  readOpencodeMessages,
  opencodeSessionId,
  type OcMessage,
} from "../core/transcript-opencode";
import { jsonChatReader } from "./registry";
import { SURVEY_AGENT, SURVEY_AGENT_CONFIG } from "../core/survey";

/** opencode message part shape for the per-turn prompt (structurally typed). */
type Part = { type?: string; text?: string };

/** The config hook's input: the host's resolved config, agents keyed by name. */
type OcConfig = { agent?: Record<string, unknown> };

/**
 * Teach the host about the survey agent (core/survey.ts spawns `opencode run --agent
 * wenyuan-survey`), so the recipe needs nothing in the user's opencode.json.
 *
 * Declared as a standalone CONST with an explicit signature rather than inlined into the returned
 * object, and that IS the point: the adapter returns a structurally typed hooks bag (the other
 * hooks take deliberately narrower params than the SDK declares, so they cannot be checked), which
 * means nothing would catch this hook being misnamed or its signature changing under us — it would
 * just silently never fire, and the survey would die on an agent the host never heard of. Pinning
 * this one hook to its own named type restores that check where it matters.
 */
const surveyAgentHook: { config: (cfg: OcConfig) => Promise<void> } = {
  config: async (cfg: OcConfig) => {
    cfg.agent ??= {};
    // Never overwrite an existing entry: a user who defined `wenyuan-survey` themselves outranks
    // us. The entry itself is cast: the host's runtime accepts permission maps with arbitrary
    // action names, wider than its published type describes.
    cfg.agent[SURVEY_AGENT] ??= SURVEY_AGENT_CONFIG as NonNullable<OcConfig["agent"]>[string];
  },
};

/** One natively registered opencode tool (structural subset of the host's tool contract). */
export interface OcTool {
  description: string;
  /** The host wraps `args` and validates arguments with it at call time. */
  args: unknown;
  execute(args: Record<string, unknown>): Promise<string>;
}

/** The named-hooks bag a v1 opencode plugin returns (the half this adapter fills). */
export interface OcHooks {
  config?: (cfg: OcConfig) => Promise<void>;
  tool: Record<string, OcTool>;
  "chat.message"?: (
    input: { sessionID?: string },
    output: { parts: Part[] }
  ) => Promise<void>;
  "experimental.chat.system.transform"?: (
    input: { sessionID?: string },
    output: { system: string[] }
  ) => Promise<void>;
  "experimental.chat.messages.transform"?: (
    input: unknown,
    output: { messages: OcMessage[] }
  ) => Promise<void>;
  event?: (input: {
    event?: { type?: string; properties?: { sessionID?: string } };
  }) => Promise<void>;
}

const textOf = (parts: Part[] | undefined) =>
  (parts || [])
    .filter((p) => p?.type === "text" && p.text)
    .map((p) => p.text as string)
    .join("\n")
    .trim();

// ── backfill: read opencode's past sessions ─────────────────────────────────────
// Same normalized JSON export every harness uses — kept here only so the real adapter is a
// complete HarnessAdapter; the registry never routes through this file to get it (see
// harness/registry.ts's getHarness("opencode")), so this line pulling in "./registry" never drags
// the host shapes along for backfill's sake.
const chatReader = jsonChatReader("opencode");

/**
 * Adapt a harness-agnostic ToolSpec (the MCP-shaped spec every harness shares) to an opencode
 * native tool. The spec states its input as plain JSON Schema (the MCP dialect this package
 * publishes everywhere); the host validates the args it is given at call time regardless, so the
 * schema object rides the same cast slot the SDK's own `args` type occupies — the host serializes
 * whatever schema object it finds there.
 */
function toOpencodeTool(spec: ToolSpec): OcTool {
  return {
    description: spec.description,
    args: spec.inputSchema as unknown as ToolInputSchema,
    async execute(args: Record<string, unknown>) {
      const r = await spec.handler(args);
      return r.content?.map((c) => c.text).join("\n") || "";
    },
  };
}

// ── runtime: opencode plugin hooks wired to the RuntimeCore ──────────────────────
function createRuntime(core: RuntimeCore): OcHooks {
  // Register the full wenyuan_* knowledge + recall suite natively (no MCP server needed).
  const tools: Record<string, OcTool> = {};
  for (const spec of core.toolSpecs()) tools[spec.name] = toOpencodeTool(spec);

  return {
    config: surveyAgentHook.config,
    tool: tools,
    // Each user turn: recall on the prompt; the injection it builds is pushed by system.transform.
    "chat.message": async (input, output) => {
      await core.onPrompt(input.sessionID, textOf(output?.parts));
    },
    // Push this turn's injection (recalled memories + attribution/user-feedback framing, plus the
    // knowledge preamble on turn 1 and the roster refresh on cadence) into the system prompt every
    // turn. opencode fires this hook with NO sessionId (input is just `{model}`), so getInjection
    // falls back to the most recent turn's block (see RuntimeCore.getInjection).
    "experimental.chat.system.transform": async (input, output) => {
      const inj = core.getInjection(input?.sessionID);
      if (inj && output?.system) output.system.push(inj);
      diag(core.harness, inj ? "inject_ok" : "inject_empty", {
        chars: inj?.length ?? 0,
        hasSession: !!input?.sessionID,
      });
    },
    // Write-back (on by default): normalize the live transcript to rich turns (text + tool calls +
    // their inline output) and hand it to core, which upserts on the server side.
    //
    // NOTE this fires while the host BUILDS a request, so `messages` never includes the reply that
    // request is about to produce — on its own it always lags a turn. `session.idle` below closes
    // that gap; this stays as the mid-session cadence path.
    "experimental.chat.messages.transform": async (_input, output) => {
      if (!core.writeBackEnabled) return;
      const msgs = output?.messages || [];
      const sid = opencodeSessionId(msgs);
      if (!sid) return;
      await core.onTranscript(sid, readOpencodeMessages(msgs), false);
    },
    // The Stop-equivalent these hosts otherwise lack: `session.idle` fires once the assistant has
    // finished, which is the only moment the completed exchange is readable. Without it a session's
    // last turn — usually its conclusion — was never retained at all.
    event: async (input) => {
      if (input?.event?.type !== "session.idle") return;
      const sid = input.event.properties?.sessionID;
      if (!sid) return;
      await core.onSessionIdle(sid);
    },
  };
}

export const opencodeAdapter: HarnessAdapter = { name: "opencode", chatReader, createRuntime };
