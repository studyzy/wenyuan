/**
 * Knowledge-page MCP tool specs — runtime SDK-free so this stays unit-testable without a real MCP
 * host.
 *
 * A host's MCP server wires the specs returned here into its tool listing. Each tool wraps one
 * client method: it never throws — a thrown client error is caught and turned into an
 * `isError:true` text result so the calling LLM sees the failure instead of the process crashing.
 *
 * The agent-facing surface is intentionally curated: grounding + capture only. Raw page CRUD
 * (create/update/delete) is deliberately NOT exposed — agents never author page structure; they
 * capture initiatives (wenyuan_capture_initiative) and pages are synthesized/maintained by the
 * server. wenyuan_ingest_document (raw-content retain) also backs the codebase survey.
 *
 * Tool input schemas are written in the MCP tool-schema shape (plain JSON Schema objects, the same
 * style src/runtime/mcp-server.ts declares) rather than through a schema-builder library: the
 * package ships no such dependency, and a three-string argument surface does not need one.
 */
import { createHash } from "node:crypto";
import { existsSync } from "node:fs";
import type { MemoryClient } from "./client";
import { applyBankConfig, DEFAULT_REFLECT_TOOL_TIMEOUT_MS, loadConfig } from "./config";
import { diagFilePath } from "./diag";
import { describeError } from "./log";
import type { PageTrigger } from "./missions";
import type { RetainStamp } from "./retain-stamp";
import { storedConfigPath } from "../config/paths";

export interface ToolResult {
  // Index signature so this structurally satisfies an MCP host's call-tool result (which carries
  // extra optional fields we don't set) — this file stays SDK-free.
  [x: string]: unknown;
  content: { type: "text"; text: string }[];
  isError?: boolean;
}

/** The four safety hints an MCP host reads to decide whether a tool call needs approval. */
export interface ToolSafetyAnnotations {
  readOnlyHint: boolean;
  destructiveHint: boolean;
  idempotentHint: boolean;
  openWorldHint: boolean;
}

const READ_ONLY_ANNOTATIONS: ToolSafetyAnnotations = {
  readOnlyHint: true,
  destructiveHint: false,
  idempotentHint: true,
  openWorldHint: false,
};

const NON_DESTRUCTIVE_WRITE_ANNOTATIONS: ToolSafetyAnnotations = {
  readOnlyHint: false,
  destructiveHint: false,
  idempotentHint: false,
  openWorldHint: false,
};

/** One string argument of a tool input schema. */
export interface ToolParam {
  type: "string";
  description?: string;
}

/** Tool input schemas, in the MCP tool-schema shape (plain JSON Schema). */
export interface ToolInputSchema {
  type: "object";
  properties: Record<string, ToolParam>;
  required?: string[];
}

/**
 * Returned alongside every search result set.
 *
 * Phrased as an obligation triggered by the CALL, not by the agent judging that it "used" a page: a
 * model that paraphrases a snippet into its own prose does not register itself as having quoted
 * anything, and silently absorbs the memory instead of crediting it.
 */
const CREDIT_REMINDER =
  "Crediting is mandatory, not a judgement call: if anything these results contribute reaches your " +
  "reply — quoted, paraphrased, or merely confirming what you were going to say — open that part " +
  'with "> 🧠 **From WenYuan memory (<page>)** — <the specific facts you drew on>". Rewriting a ' +
  "snippet in your own words does not make it yours. If none of them bear on the turn, ignore them " +
  "silently — an unhelpful search needs no mention. These are past records: check a claim that " +
  "something was fixed or works against the code before relying on it.";

export interface ToolSpec {
  name: string;
  description: string;
  inputSchema: ToolInputSchema;
  /**
   * Safety metadata published verbatim as the tool's MCP annotations.
   *
   * Required, not optional: it is not cosmetic. Hosts gate every MCP tool lacking a coherent
   * read-only annotation behind an approval prompt, so in a headless runtime an unannotated tool
   * is REJECTED outright. Reads get READ_ONLY_ANNOTATIONS; the two writes get
   * NON_DESTRUCTIVE_WRITE_ANNOTATIONS so clients still gate them, but for the right reason.
   */
  annotations: ToolSafetyAnnotations;
  handler: (args: Record<string, unknown>) => Promise<ToolResult>;
}

/**
 * The status counters wenyuan_sync_status reports, declared HERE (not imported from the status
 * module) so the dependency points one way: this module defines the narrow surface it needs, and
 * the background layer satisfies it structurally — importing the status module from here would
 * close a cycle through the injection text it also feeds.
 */
export interface SyncStatusCounters {
  bank: string;
  /** The aggregated commit-message seed document exists. */
  gitlogPresent: boolean;
  /** Commits ingested individually with full diffs. */
  gitDiffDocs: number;
  /** How many per-commit diffs the deepen engine targets; null when the repo is unknown. */
  gitDiffTarget: number | null;
  /** Past/live conversations in the bank. */
  chatDocs: number;
  pagesCount: number;
  /** HEAD sha at the last codebase survey start; null = never started. */
  surveyBaseline: string | null;
  /** Findings documents present (of 4) — completion signal, not just started. */
  surveyDocs: number;
  /** Commits since that baseline; null = no baseline / no repo. */
  surveyCommitsBehind: number | null;
  /** Extraction ops still running; null if the server can't report. */
  activeOps: number | null;
  synced: boolean;
}

/** Resolves the counters for one bank from one working directory. Wired in by the lifecycle
 *  composition; the status module's own function is a three-argument shape over the client, so
 *  callers close over the client when adapting it to this. */
export type SyncStatusFn = (bankId: string, repoDir: string) => Promise<SyncStatusCounters>;

function ok(value: unknown): ToolResult {
  return { content: [{ type: "text", text: JSON.stringify(value, null, 2) }] };
}

function err(e: unknown): ToolResult {
  const message = describeError(e);
  return { content: [{ type: "text", text: JSON.stringify({ error: message }) }], isError: true };
}

/** Wrap a handler body so a thrown client error always becomes an isError:true result, never a throw. */
function guarded(
  fn: (args: Record<string, unknown>) => Promise<unknown>
): (args: Record<string, unknown>) => Promise<ToolResult> {
  return async (args) => {
    try {
      return ok(await fn(args));
    } catch (e) {
      return err(e);
    }
  };
}

/** Build the knowledge-page + recall MCP tool specs, bound to one client/bank. */
export function buildKnowledgeTools(
  client: MemoryClient,
  bankId: string,
  opts: {
    repoDir?: string;
    harness?: string;
    stampFor?: () => RetainStamp;
    /** Refresh policy for a page wenyuan_capture_initiative creates (core/missions). */
    pageTrigger?: PageTrigger;
    /** The status counter behind wenyuan_sync_status — see SyncStatusFn. */
    syncStatus?: SyncStatusFn;
    /** How long wenyuan_reflect waits on the server (cfg.reflectToolTimeoutMs). Must be
     *  threaded in by every caller: left unset, the client falls back to a 120s deadline that
     *  aborts high-budget synthesis on a populated bank mid-flight. */
    reflectTimeoutMs?: number;
    /** Reflect budget for wenyuan_reflect (cfg.reflectBudget, default "high"). */
    reflectBudget?: "low" | "mid" | "high";
    /** cfg.toolGuideExtra, added after the crediting note so it lands with the results too. */
    toolGuideExtra?: string;
  } = {}
): ToolSpec[] {
  const extra = opts.toolGuideExtra?.trim();
  const crediting = extra ? `${CREDIT_REMINDER} ${extra}` : CREDIT_REMINDER;
  return [
    {
      name: "wenyuan_sync_status",
      description:
        "Report whether this repo's memory bank is in sync: gitlog seed present, how much recent " +
        "history has been deepened with full diffs, conversations ingested, knowledge pages " +
        "created, and extractions still running. `synced: true` means the seeded memory is fully " +
        "queryable. Ingestion is automatic and background — if not synced, it is in progress; " +
        "nothing to run.",
      inputSchema: { type: "object", properties: {} },
      annotations: READ_ONLY_ANNOTATIONS,
      handler: async () => {
        try {
          if (!opts.syncStatus) throw new Error("sync status is not wired into this session");
          return ok(await opts.syncStatus(bankId, opts.repoDir ?? process.cwd()));
        } catch (e) {
          return err(e);
        }
      },
    },
    {
      name: "wenyuan_diagnose",
      description:
        "Report safe WenYuan runtime diagnostics for this coding-agent session: resolved bank, " +
        "workspace, harness, config location, API endpoint, and non-secret environment overrides. " +
        "Use this when memory, hooks, MCP tools, or configuration appear not to work. Tokens and " +
        "other secret values are never returned.",
      inputSchema: { type: "object", properties: {} },
      annotations: READ_ONLY_ANNOTATIONS,
      handler: async () => {
        // The same override loadConfig itself honours, so the reported path is the file actually read.
        const configPath = process.env.WENYUAN_AGENTS_CONFIG || storedConfigPath();
        const harness = opts.harness ?? "unknown";
        // Resolve through the SAME pipeline the host used — including the `banks.<id>` section for
        // the bank this client is bound to, which may carry its own apiToken/apiUrl. A bare
        // loadConfig() would report a mismatch for every per-bank credential.
        const cfg = applyBankConfig(loadConfig({ harness: opts.harness }), bankId, opts.repoDir).cfg;
        return ok({
          bank_id: bankId,
          harness,
          workspace: opts.repoDir ?? process.cwd(),
          config: {
            path: configPath,
            exists: existsSync(configPath),
            api_url: cfg.apiUrl,
            api_token_configured: Boolean(cfg.apiToken),
            disabled: cfg.disabled,
          },
          // What the LIVE client is signing with, which is not the same question as what the file
          // says: a long-lived host can keep a credential the config has already replaced, and
          // reporting the file alone paints that state as perfectly healthy. Booleans only — the
          // token value is never returned.
          credential: {
            api_token_in_use: Boolean(client.apiToken),
            api_token_matches_config: client.apiToken === cfg.apiToken,
          },
          environment: {
            config_override: Boolean(process.env.WENYUAN_AGENTS_CONFIG),
            // Mirrors the hook contract's kill switch; kept literal to avoid a core→runtime import edge.
            hooks_disabled: Boolean(process.env.WENYUAN_DISABLE_HOOKS),
            log_level: process.env.WENYUAN_AGENTS_LOG_LEVEL ?? null,
            diagnostics_file: diagFilePath(),
            channel_id_configured: Boolean(process.env.WENYUAN_AGENTS_CHANNEL_ID),
            user_id_configured: Boolean(process.env.WENYUAN_AGENTS_USER_ID),
          },
        });
      },
    },
    {
      name: "wenyuan_search_knowledge_pages",
      description:
        "Search this repository's WenYuan knowledge pages for content relevant to a query — " +
        "hybrid full-text + semantic search, server-side. Call this when the user's question may " +
        "be answered by the project's accumulated knowledge (architecture, conventions, decisions, " +
        "initiatives) rather than by reading code. Returns ranked pages with a relevance snippet; " +
        // Same sentence the payload carries, from the same constant: two copies of a rule this
        // fiddly drift apart, and the description is what a host shows when the tool is listed.
        "read a full page with wenyuan_read_knowledge_page. " +
        crediting,
      inputSchema: {
        type: "object",
        properties: { query: { type: "string", description: "what to look for" } },
        required: ["query"],
      },
      annotations: READ_ONLY_ANNOTATIONS,
      handler: async (args) => {
        try {
          // Limit comes from the client (`pageSearchLimit`), so the tool and the hook's injection
          // can never drift apart — this used to pass its own literal 3.
          const hits = await client.searchKnowledgePages(args.query as string);
          // No `score`. The server fuses full-text and vector search with reciprocal rank fusion,
          // so the number is ~1/(60+rank) summed over two retrievers: a perfect top hit scores
          // about 0.03 and nothing ever approaches 1. Handed that, a model reads a strong match as
          // 3% relevant and discounts it. The hits arrive in rank order, which is the ranking that
          // means something here.
          // The reminder rides WITH the hits, not only in the session guide: the guide has scrolled
          // far up the context by the time a search lands mid-session, and the instruction that
          // arrives at the same moment as the results is the one that can still be acted on.
          return ok({
            pages: hits.map((h) => ({
              page: h.name,
              page_id: h.id,
              // The question the page answers — the same `description` the list and read tools
              // carry, so a hit says what the page is FOR, not just how it opens.
              ...(h.source_query ? { description: h.source_query } : {}),
              snippet: h.snippet,
            })),
            crediting,
          });
        } catch (e) {
          return err(e);
        }
      },
    },
    {
      name: "wenyuan_list_knowledge_pages",
      description:
        "List this repository's WenYuan knowledge pages — curated, continuously-updated " +
        "summaries of the project's durable knowledge (architecture, components, conventions, key " +
        "decisions, and in-flight initiatives). Returns each page's id, title, and a one-line " +
        "description of what it covers. Call this at the start of any non-trivial task, and again " +
        "periodically in long sessions, to see what the project already knows before you read code " +
        "or ask the user. The list changes as work is captured, so re-check it occasionally.",
      inputSchema: { type: "object", properties: {} },
      annotations: READ_ONLY_ANNOTATIONS,
      handler: guarded(async () => client.listPages()),
    },
    {
      name: "wenyuan_read_knowledge_page",
      description:
        "Read the full content of one knowledge page by its id (from " +
        "wenyuan_list_knowledge_pages). Call this whenever a listed page is relevant to what " +
        "you're about to do — e.g. read Conventions before writing new code, Component map before " +
        "changing a subsystem, or an initiative's page before continuing that feature. A page may " +
        "contain [[page:<id>]] links to related pages; follow one by calling this tool again with " +
        "that id. Prefer reading a page over re-deriving the same understanding from source.",
      inputSchema: {
        type: "object",
        properties: { page_id: { type: "string" } },
        required: ["page_id"],
      },
      annotations: READ_ONLY_ANNOTATIONS,
      handler: guarded(async (args) => client.getPage(args.page_id as string)),
    },
    {
      name: "wenyuan_reflect",
      description:
        "Deep memory reasoning: an agentic synthesis over this repository's FULL memory (git " +
        "decisions, past sessions, ingested knowledge) that answers WHY questions — the past " +
        "decision and exact rule/values that explain a behavior, bug, or convention. Slower than " +
        "wenyuan_search_knowledge_pages (several seconds): reach for it when pages are too " +
        "shallow and you need the root cause or the decided literals. When the answer informs " +
        'your reply, credit it visibly with a blockquote header: "> 🧠 **From WenYuan memory** — <summary>".',
      inputSchema: {
        type: "object",
        properties: { query: { type: "string", description: "the question to reason over memory about" } },
        required: ["query"],
      },
      annotations: READ_ONLY_ANNOTATIONS,
      handler: guarded(async (args) =>
        client.reflect(args.query as string, {
          budget: opts.reflectBudget ?? "high",
          timeoutMs: opts.reflectTimeoutMs ?? DEFAULT_REFLECT_TOOL_TIMEOUT_MS,
        })
      ),
    },
    {
      name: "wenyuan_capture_initiative",
      description:
        "Record a new feature or initiative as a tracked knowledge page, so future sessions know it " +
        "exists and can build on it — and keep that page tracking the plan as it moves.\n\n" +
        "WHEN TO CALL:\n" +
        "- Starting one: right after the user approves a plan or finishes brainstorming a new feature/" +
        "capability and you are about to start implementing — BEFORE you write any code. Leave " +
        "relates_to_page_id empty.\n" +
        "- Plan changed: call it AGAIN — mid-implementation is fine and expected — whenever the goal, " +
        "scope, or rationale of an initiative you already captured materially changes (an approach is " +
        "dropped, scope grows or shrinks, the why is rewritten). Pass relates_to_page_id = that " +
        "initiative's page id so the change lands on the existing page. Never mint a second page for the " +
        "same initiative.\n\n" +
        "WHEN TO SKIP: bug fixes, small tweaks, refactors, chores, and trivial course-corrections that " +
        "leave the goal intact are not initiatives — skip them.\n\n" +
        "- title: short, specific name (e.g. 'Newsletter refinement chat'). On a recapture, reuse the " +
        "initiative's existing title.\n" +
        "- summary: 2-3 sentences on what you're building and why — the CURRENT intent, not the " +
        "originally approved plan (on a recapture, say what changed and why).\n" +
        "- relates_to_page_id: leave empty for a new initiative. Set it to an existing initiative's page " +
        "id — from wenyuan_list_knowledge_pages, or the id this tool returned earlier — to record a " +
        "plan change or an enhancement to it.\n\n" +
        "Returns the page id. The page is generated for you — you never format one yourself.",
      inputSchema: {
        type: "object",
        properties: {
          title: { type: "string" },
          summary: { type: "string" },
          relates_to_page_id: { type: "string" },
        },
        required: ["title", "summary"],
      },
      annotations: NON_DESTRUCTIVE_WRITE_ANNOTATIONS,
      handler: guarded(async (args) =>
        client.captureInitiative({
          title: args.title as string,
          summary: args.summary as string,
          relatesToPageId: args.relates_to_page_id as string | undefined,
          ...(opts.stampFor ? { stamp: opts.stampFor() } : {}),
          ...(opts.pageTrigger ? { pageTrigger: opts.pageTrigger } : {}),
        })
      ),
    },
    {
      name: "wenyuan_ingest_document",
      description:
        "Save an external document or a block of durable notes/findings into this repository's " +
        "memory so it informs future recall and pages. Use for design notes, research, or " +
        "reference material you want remembered — not for the conversation you're already in " +
        "(that's captured automatically at session end). This is ALSO the correction mechanism: " +
        "when you verify that a retrieved memory is wrong or outdated, ingest a document titled " +
        "'Correction: <topic>' stating what memory claimed, what is actually true, and the " +
        "evidence — the newer fact supersedes the stale one in future retrieval.",
      inputSchema: {
        type: "object",
        properties: { title: { type: "string" }, content: { type: "string" } },
        required: ["title", "content"],
      },
      annotations: NON_DESTRUCTIVE_WRITE_ANNOTATIONS,
      handler: guarded(async (args) => {
        const title = args.title as string;
        const content = args.content as string;
        const slug = title
          .toLowerCase()
          .replace(/[^a-z0-9]+/g, "-")
          .replace(/^-+|-+$/g, "");
        // ASCII-only slugs erased non-Latin titles (or shared the "doc" fallback), overwriting
        // unrelated documents. Hash the original title, not its content, so re-ingestion still
        // updates it; "--" cannot occur in a legacy slug.
        const docId =
          /[^\x00-\x7f]/.test(title) || !slug
            ? `${slug || "doc"}--${createHash("sha256").update(title).digest("hex")}`
            : slug;
        const stamp = opts.stampFor?.();
        const metadata = {
          ...stamp?.metadata,
          ...(opts.harness ? { harness: opts.harness } : {}),
        };
        await client.retain(
          content,
          "ingested document",
          docId,
          [
            ...new Set([
              ...(stamp?.tags ?? []),
              "source:upload",
              ...(opts.harness ? [`harness:${opts.harness}`] : []),
            ]),
          ],
          "document",
          Object.keys(metadata).length ? { metadata } : {}
        );
        return { ok: true, doc_id: docId };
      }),
    },
  ];
}
