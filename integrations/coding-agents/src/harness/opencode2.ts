/**
 * opencode2 (opencode v2) harness adapter — full parity with the v1 opencode plugin.
 *
 * opencode v2 is a ground-up rewrite of the plugin API, so NOTHING from harness/opencode.ts
 * carries over. A v1 plugin is a function returning a bag of named hooks; a v2 plugin is
 * `{id, setup(ctx)}` where `ctx` hands out per-domain registration calls. The mapping this file
 * implements:
 *
 *   v1 `chat.message`                       -> `ctx.session.hook("prompt")`
 *   v1 `experimental.chat.system.transform` -> `ctx.session.hook("context")` (push a SystemPart)
 *   v1 `tool: {...}` (native tools)         -> `ctx.tool.transform(draft => draft.add(...))`
 *   v1 `event` (session.idle)               -> `ctx.event.subscribe()` async iterator
 *   v1 `client.session.messages()`          -> `ctx.session.context({sessionID})`
 *
 * Everything behind those five seams is the harness-agnostic RuntimeCore, exactly as for v1, so
 * opencode2 gets the same surface: per-turn recall + attribution/user-feedback injection, the
 * wenyuan_* knowledge tools, cold-check auto-seed and rich write-back.
 *
 * Two deliberate differences from the v1 adapter, both forced by the host:
 *
 *  - NO survey agent. v1 taught the host about a read-only `wenyuan-survey` agent through its
 *    `config` hook (see core/survey.ts). v2's `ctx.agent.transform` draft exposes only
 *    list/get/default/update/remove — a plugin cannot DEFINE an agent — and an agent declared in
 *    the config file is not a sandbox either: the user's own global `permissions` are appended
 *    AFTER an agent's rules. The survey reads untrusted repo files, so without a guaranteed
 *    read-only boundary it must not run under this host — opencode2 falls back to another
 *    installed agent's CLI, the same treatment kilo/cline/cursor/dcode already get (core/survey.ts).
 *    Revisit if v2 gains plugin-defined agents.
 *
 *  - NO toast. v2's plugin client can OBSERVE `tui.toast.show` events but cannot publish one, so
 *    the seed banner is logged rather than shown. `RuntimeCore`'s notifier is optional and
 *    fail-open.
 *
 * One capability the v1 adapter has is deliberately absent because v2 makes it redundant, not
 * because it was forgotten: v1 also drove `RuntimeCore.onTranscript` from its messages.transform
 * hook, as a mid-session cadence, because its `session.idle` was the ONLY moment a completed reply
 * was readable and a long session would otherwise write back nothing until it ended. v2 raises
 * `session.execution.succeeded` after every assistant turn, so the idle path already fires once per
 * turn — from the authoritative post-reply transcript rather than the pre-reply list the context
 * hook sees. Adding the cadence path on top would only re-retain the same turns.
 *
 * Every host shape is STRUCTURALLY typed — no SDK import, no version to keep in step.
 */
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { resolveHostMemory } from "../core/host-client";
import { describeError } from "../core/log";
import { diag } from "../core/diag";
import { RuntimeCore } from "../core/runtime";
import type { ToolSpec } from "../core/knowledge-tools";
import { readOpencode2Messages, type Oc2Message } from "../core/transcript-opencode2";
import { PACKAGED_SKILL_DIRECTORY, PACKAGED_SKILL_FILE, packagedSkillDirectory } from "../hosts/skills";
import { resolveProjectDirectory } from "./plugin-entry";
import { SKILL_DIRECTORY_NAME } from "../core/skill-dirs";

/**
 * Events that mean "the assistant has finished and the completed exchange is readable" — the
 * Stop-equivalent these hosts otherwise lack.
 *
 * Both are needed. `session.idle` is what the interactive TUI settles on; a one-shot `opencode2
 * run` exits before it ever fires and emits only `session.execution.succeeded` (verified against
 * the v2 beta). Listening to both is safe: `RuntimeCore.onSessionIdle` only retains when the
 * transcript actually grew past what it last wrote, so a turn that raises both costs one retain.
 */
const IDLE_EVENTS = new Set([
  "session.idle",
  "session.execution.succeeded",
  "session.execution.failed",
  "session.execution.interrupted",
]);

/** The companion skill's directory name everywhere else (core/skill-dirs.ts), reused as the id and
 *  name this host registers it under so the two routes name the same skill. */
const SKILL_ID = SKILL_DIRECTORY_NAME;

/** The packaged companion skill, split into what the host's skill registry asks for. */
export interface PackagedSkill {
  path: string;
  description: string;
  content: string;
}

/**
 * Read the packaged skill/ directory shipped with this build.
 *
 * Fail-open: an undefined return means "no packaged skill beside this build" (a dev tree before
 * the skill build) and the caller skips registration — never a broken plugin.
 */
export function readPackagedSkill(
  srcDir: string | null = packagedSkillDirectory()
): PackagedSkill | undefined {
  if (!srcDir) return undefined;
  try {
    const path = join(srcDir, PACKAGED_SKILL_FILE);
    const raw = readFileSync(path, "utf8");
    const frontmatter = /^---\r?\n([\s\S]*?)\r?\n---\r?\n?/.exec(raw);
    return {
      path,
      description: frontmatter
        ? (/^description:\s*(.+)$/m.exec(frontmatter[1] ?? "")?.[1] ?? "")
        : "",
      content: frontmatter ? raw.slice(frontmatter[0].length) : raw,
    };
  } catch {
    return undefined;
  }
}

/** Structural subset of the v2 event envelope we act on. */
interface Oc2Event {
  type?: string;
  data?: { sessionID?: string };
}

/** The setup context the host hands a plugin — the half of it this adapter uses. */
export interface Oc2Context {
  location?: { directory?: string; project?: { directory?: string } };
  session: {
    hook(name: string, cb: unknown): Promise<unknown>;
    context(o: { sessionID: string }): Promise<unknown>;
  };
  tool: { transform(cb: (draft: { add(tool: unknown): void }) => void): Promise<unknown> };
  skill?: { transform(cb: (draft: { add(skill: unknown): void }) => void): Promise<unknown> };
  event: { subscribe(opts?: { signal?: AbortSignal }): AsyncIterable<unknown> };
}

/** A v2 plugin: an id plus a setup that registers against the context. */
export interface Oc2Plugin {
  id: string;
  setup(ctx: Oc2Context): Promise<void | (() => void)>;
}

/**
 * Adapt a harness-agnostic ToolSpec (the MCP-shaped spec every harness shares) to a v2 tool.
 *
 * Two v2 specifics:
 *  - `input` is a whole schema, not v1's raw shape. This package states tool inputs as plain JSON
 *    Schema (the MCP dialect), which is exactly the shape v2's registry serializes for the model;
 *    no conversion is needed or possible (the package carries no schema library by design).
 *  - The spec's MCP safety `annotations` have no slot in v2's tool contract, so they are dropped
 *    here — the same as every other native-tool harness, which publishes them only over MCP.
 *  - `options.codemode: false` is REQUIRED, not decoration. A tool left on the default is offered
 *    to the model only through v2's `execute` code-execution tool, so a model that calls it by
 *    name gets "Unknown tool: wenyuan_…" — which is exactly how every wenyuan_* tool behaves
 *    without this flag, since the skill and the injected preamble both tell the model to call them
 *    by name.
 */
export function toOpencode2Tool(spec: ToolSpec): Record<string, unknown> {
  return {
    name: spec.name,
    description: spec.description,
    input: spec.inputSchema,
    options: { codemode: false as const },
    async execute(input: unknown) {
      const r = await spec.handler((input ?? {}) as Record<string, unknown>);
      return { content: r.content?.map((c) => c.text).join("\n") || "" };
    },
  };
}

/**
 * Publish the packaged companion skill into this host's skill registry.
 *
 * Every other skills-capable host gets the skill as a COPY our installer drops in its skills
 * directory (core/skill-dirs.ts). opencode2 has none to drop into: it discovers other hosts'
 * skills roots, which our uninstall removes by fixed directory name — so writing there would make
 * uninstalling another host take opencode2's skill with it. An opencode2-only install therefore
 * shipped the tools with no skill at all.
 *
 * v2 can register one in memory instead, so nothing is written to disk and no second copy can go
 * stale: the content is read from the PACKAGE at setup, which is what makes a package upgrade
 * upgrade the skill here too. Fail-open like every other seam in this adapter — an older v2
 * without `ctx.skill` loses the skill, never the memory. A draft the host's schema REJECTS is
 * worse: the host disables the whole plugin, not just the skill (a missing `path` did exactly
 * that), so the draft must carry every field the host requires.
 */
async function registerCompanionSkill(ctx: Oc2Context, harness: string): Promise<void> {
  const skill = readPackagedSkill();
  if (!skill) return;
  try {
    await ctx.skill?.transform((draft) =>
      draft.add({ id: SKILL_ID, name: SKILL_ID, ...skill })
    );
    diag(harness, "skill_registered", { id: SKILL_ID, dir: PACKAGED_SKILL_DIRECTORY });
  } catch (e) {
    diag(harness, "skill_register_failed", { error: describeError(e) });
  }
}

/**
 * Wire a RuntimeCore onto one opencode2 plugin context and return the teardown.
 *
 * Split out of `createOpencode2PluginEntry` so the hook wiring can be exercised against a fake
 * context without a config file, a bank or a server — the same seam `createClineHooks` gives the
 * Cline adapter.
 */
export async function wireOpencode2Runtime(
  core: RuntimeCore,
  ctx: Oc2Context
): Promise<() => void> {
  const harness = core.harness;

  // Let the runtime READ the transcript back once the assistant has finished. `session.context`
  // returns the session's messages in the same shape readOpencode2Messages consumes.
  core.setTranscriptSource(async (sessionID) =>
    readOpencode2Messages((await ctx.session.context({ sessionID })) as Oc2Message[])
  );

  // Register the full wenyuan_* knowledge + recall suite natively (no MCP server needed).
  await ctx.tool.transform((draft) => {
    for (const spec of core.toolSpecs()) draft.add(toOpencode2Tool(spec));
  });

  await registerCompanionSkill(ctx, harness);

  // Sessions THIS plugin instance owns. See the event loop below for why the write-back cannot do
  // without it.
  const ownSessions = new Set<string>();

  // Each user turn: recall on the prompt; the injection it builds is pushed by the context hook.
  await ctx.session.hook("prompt", async (input: unknown) => {
    const { sessionID, prompt } = input as { sessionID: string; prompt?: { text?: string } };
    ownSessions.add(sessionID);
    await core.onPrompt(sessionID, prompt?.text ?? "");
  });

  // Push this turn's injection (recalled memories + attribution/user-feedback framing, plus the
  // knowledge preamble on turn 1 and the roster refresh on cadence) into the system prompt every
  // turn. Unlike v1's system.transform this hook DOES carry the session id, so the lookup is
  // always session-keyed.
  await ctx.session.hook("context", (input: unknown) => {
    const { sessionID, system } = input as {
      sessionID: string;
      system: { type: string; text: string }[];
    };
    const inj = core.getInjection(sessionID);
    if (inj && system) system.push({ type: "text", text: inj });
    diag(harness, inj ? "inject_ok" : "inject_empty", { chars: inj?.length ?? 0 });
  });

  // Write-back (on by default). Refetching on idle — rather than replaying the list the context
  // hook saw — is the whole point: that list is built BEFORE the reply, so it always lags a turn
  // and a session's final exchange would never be retained.
  //
  // `ownSessions` is a HARD BANK-ISOLATION GUARD, not an optimisation. v2's background service
  // hosts every open project at once, and unlike the session hooks — which fire only for the
  // location that loaded this plugin instance — `event.subscribe()` is GLOBAL: a plugin loaded for
  // project A receives session.idle for a session running in project B, carrying no location to
  // tell them apart (the envelope's `location` arrives undefined; verified against the v2 beta).
  // Acting on those ids would fetch another project's transcript and retain it into THIS project's
  // bank. So only sessions our own scoped prompt hook has admitted are eligible.
  const events = new AbortController();
  void (async () => {
    for await (const event of ctx.event.subscribe({ signal: events.signal })) {
      const e = event as Oc2Event;
      if (!e.type || !IDLE_EVENTS.has(e.type)) continue;
      const sessionID = e.data?.sessionID;
      if (!sessionID || !ownSessions.has(sessionID)) continue;
      diag(harness, "idle_signal", { signal: e.type, session: sessionID });
      // Deliberately NOT awaited: a retain must never stall delivery of the next event.
      void core.onSessionIdle(sessionID);
    }
    // Falling out of the loop (rather than throwing) means the host closed the stream — teardown.
    // A stream that DIES takes write-back with it silently, which is exactly the failure a
    // memory-less session must not be able to hide, so it goes in the diagnostic trail too.
    diag(harness, "idle_stream_closed", {});
  })().catch((e) => diag(harness, "idle_stream_failed", { error: describeError(e) }));

  return () => events.abort();
}

/**
 * Build the default export for an opencode2 host. `harness` is the name the host is known by, used
 * for config lookup (`harnesses.<name>`), `{harness}` bank templating and log scoping.
 */
export function createOpencode2PluginEntry(harness: string): Oc2Plugin {
  return {
    id: `wenyuan-${harness}`,
    async setup(ctx) {
      // v2 splits v1's single `{worktree, directory}` into a session cwd plus the project root;
      // `location.directory` is the worktree analogue, so it keeps precedence (and the same
      // filesystem-root guard) with the project root as the fallback.
      const projectDir = resolveProjectDirectory({
        worktree: ctx.location?.directory,
        directory: ctx.location?.project?.directory,
      });
      const dir = projectDir || process.cwd();
      const { cfg, bankId, client } = resolveHostMemory(harness, dir);
      // Global switch, per-bank opt-out and optInOnly all land here: inert plugin, same agent, no
      // memory (baseline parity).
      if (cfg.disabled) return;
      const core = new RuntimeCore(client, bankId, cfg, harness, dir);
      const cleanup = await wireOpencode2Runtime(core, ctx);

      // SessionStart-equivalent: cold-check the bank and kick off the background engine.
      // Fire-and-forget — this host BLOCKS ITS BOOT on plugin setup, so it must never gate startup
      // on a network round-trip, and onPrompt needs nothing from it (it builds its own preamble).
      void core.seedIfCold(projectDir);

      return cleanup;
    },
  };
}
