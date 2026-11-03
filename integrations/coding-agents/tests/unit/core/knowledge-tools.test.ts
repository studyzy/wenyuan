import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { buildKnowledgeTools } from "../../../src/core/knowledge-tools";
import { DEFAULT_REFLECT_TOOL_TIMEOUT_MS } from "../../../src/core/config";
import type { MemoryClient } from "../../../src/core/client";

/** Minimal stub of the client surface the tools call — no network, no server. */
function stubClient(overrides: Partial<Record<string, ReturnType<typeof vi.fn>>> = {}) {
  return {
    listPages: vi.fn(async () => ({ pages: [] })),
    getPage: vi.fn(async (id: string) => ({ id })),
    searchKnowledgePages: vi.fn(async () => []),
    reflect: vi.fn(async (_q: string, _o: unknown) => "the decided rule is X=3"),
    captureInitiative: vi.fn(async (_a: unknown) => ({ page_id: "initiative-x" })),
    retain: vi.fn(async (..._args: unknown[]) => undefined),
    ...overrides,
  } as unknown as MemoryClient;
}

function findTool(tools: ReturnType<typeof buildKnowledgeTools>, name: string) {
  const t = tools.find((t) => t.name === name);
  if (!t) throw new Error(`tool not found: ${name}`);
  return t;
}

const EXPECTED_TOOLS = [
  "wenyuan_sync_status",
  "wenyuan_diagnose",
  "wenyuan_search_knowledge_pages",
  "wenyuan_list_knowledge_pages",
  "wenyuan_read_knowledge_page",
  "wenyuan_reflect",
  "wenyuan_capture_initiative",
  "wenyuan_ingest_document",
];

describe("buildKnowledgeTools", () => {
  it("returns exactly the eight expected tools (as a set)", () => {
    const client = stubClient();
    const tools = buildKnowledgeTools(client, "repo-a");
    expect(tools.map((t) => t.name).sort()).toEqual([...EXPECTED_TOOLS].sort());
  });

  it("wenyuan_sync_status is the FIRST tool in the list", () => {
    const tools = buildKnowledgeTools(stubClient(), "repo-a");
    expect(tools[0]?.name).toBe("wenyuan_sync_status");
  });

  it("declares safety annotations on every tool — reads read-only, the two writes non-destructive", () => {
    const tools = buildKnowledgeTools(stubClient(), "repo-a");
    for (const t of tools) {
      const write = t.name === "wenyuan_capture_initiative" || t.name === "wenyuan_ingest_document";
      expect(t.annotations).toEqual({
        readOnlyHint: !write,
        destructiveHint: false,
        idempotentHint: !write,
        openWorldHint: false,
      });
    }
  });

  it("does not expose the removed raw page-CRUD tools", () => {
    const names = buildKnowledgeTools(stubClient(), "repo-a").map((t) => t.name);
    for (const gone of ["create_page", "update_page", "delete_page"]) {
      expect(names).not.toContain(gone);
    }
  });

  describe("wenyuan_sync_status", () => {
    it("returns the counters resolved for the given repoDir", async () => {
      const repoDir = mkdtempSync(join(tmpdir(), "wenyuan-status-nogit-"));
      try {
        const counters = {
          bank: "repo-a",
          gitlogPresent: true,
          gitDiffDocs: 2,
          gitDiffTarget: null,
          surveyBaseline: null,
          surveyDocs: 0,
          surveyCommitsBehind: null,
          chatDocs: 1,
          pagesCount: 1,
          activeOps: 0,
          synced: true,
        };
        const syncStatus = vi.fn(async () => counters);
        const tools = buildKnowledgeTools(stubClient(), "repo-a", { repoDir, syncStatus });
        const result = await findTool(tools, "wenyuan_sync_status").handler({});
        expect(result.isError).toBeFalsy();
        expect(JSON.parse(result.content[0]?.text as string)).toEqual(counters);
        expect(syncStatus).toHaveBeenCalledWith("repo-a", repoDir);
      } finally {
        rmSync(repoDir, { recursive: true, force: true });
      }
    });

    it("falls back to the process working directory when no repoDir is given", async () => {
      const syncStatus = vi.fn(async () => ({
        bank: "repo-a",
        gitlogPresent: false,
        gitDiffDocs: 0,
        gitDiffTarget: null,
        surveyBaseline: null,
        surveyDocs: 0,
        surveyCommitsBehind: null,
        chatDocs: 0,
        pagesCount: 0,
        activeOps: 0,
        synced: false,
      }));
      const tools = buildKnowledgeTools(stubClient(), "repo-a", { syncStatus });
      await findTool(tools, "wenyuan_sync_status").handler({});
      expect(syncStatus).toHaveBeenCalledWith("repo-a", process.cwd());
    });

    it("returns isError:true when the counter throws", async () => {
      const tools = buildKnowledgeTools(stubClient(), "repo-a", {
        syncStatus: vi.fn(async () => {
          throw new Error("server down");
        }),
      });
      const result = await findTool(tools, "wenyuan_sync_status").handler({});
      expect(result.isError).toBe(true);
      expect(JSON.parse(result.content[0]?.text as string)).toEqual({ error: "server down" });
    });

    it("returns isError:true when no counter is wired into the session", async () => {
      const tools = buildKnowledgeTools(stubClient(), "repo-a");
      const result = await findTool(tools, "wenyuan_sync_status").handler({});
      expect(result.isError).toBe(true);
      expect(JSON.parse(result.content[0]?.text as string)).toMatchObject({ error: expect.any(String) });
    });
  });

  describe("wenyuan_diagnose", () => {
    const ENV_KEYS = [
      "WENYUAN_AGENTS_CONFIG",
      "WENYUAN_AGENTS_API_TOKEN",
      "WENYUAN_AGENTS_API_URL",
      "WENYUAN_AGENTS_DISABLED",
      "WENYUAN_AGENTS_HARNESS",
      "WENYUAN_AGENTS_LOG_LEVEL",
      "WENYUAN_AGENTS_CHANNEL_ID",
      "WENYUAN_AGENTS_USER_ID",
      "WENYUAN_DISABLE_HOOKS",
      "WENYUAN_CONFIG",
      "CLAUDE_PROJECT_DIR",
    ] as const;
    let saved: Record<string, string | undefined>;
    let configPath: string;

    beforeEach(() => {
      saved = {};
      for (const k of ENV_KEYS) {
        saved[k] = process.env[k];
        delete process.env[k];
      }
      // A config location that does not exist: the diagnose output must be a function of the
      // environment the test set up, never of the developer's real ~/.wenyuan/config.json.
      configPath = join(tmpdir(), `wenyuan-diagnose-${process.ppid}.json`);
      process.env.WENYUAN_AGENTS_CONFIG = configPath;
    });

    afterEach(() => {
      for (const k of ENV_KEYS) {
        const prior = saved[k];
        if (prior === undefined) delete process.env[k];
        else process.env[k] = prior;
      }
    });

    it("returns safe runtime configuration without touching the client", async () => {
      const client = stubClient();
      const tools = buildKnowledgeTools(client, "repo-a");
      const result = await findTool(tools, "wenyuan_diagnose").handler({});
      expect(result.isError).toBeFalsy();
      const report = JSON.parse(result.content[0]?.text as string);
      expect(report).toMatchObject({
        bank_id: "repo-a",
        harness: "unknown",
        workspace: process.cwd(),
        config: {
          path: configPath,
          exists: false,
          api_token_configured: false,
          disabled: false,
        },
        credential: { api_token_in_use: false },
        environment: {
          config_override: true,
          hooks_disabled: false,
          log_level: null,
          channel_id_configured: false,
          user_id_configured: false,
        },
      });
      expect(report.environment.diagnostics_file).toBeTruthy();
      expect(client.listPages).not.toHaveBeenCalled();
      expect(client.getPage).not.toHaveBeenCalled();
    });

    it("reports the credential in USE, not only the one on disk", async () => {
      // Diagnose read the config FILE and called a host healthy while its live client was signing
      // with a credential that no longer existed — misdirecting the one investigation this tool
      // exists to guide.
      const client = stubClient();
      (client as unknown as { apiToken: string }).apiToken = "credential-the-host-started-with";
      const tool = findTool(buildKnowledgeTools(client, "repo-a"), "wenyuan_diagnose");

      const report = JSON.parse((await tool.handler({})).content[0]?.text as string);
      expect(report.credential).toEqual({
        api_token_in_use: true,
        api_token_matches_config: false,
      });
      // Booleans only — the value itself must never leave the process.
      expect(JSON.stringify(report)).not.toContain("credential-the-host-started-with");
    });
  });

  it("wenyuan_search_knowledge_pages calls the server hybrid search and returns ranked hits", async () => {
    const client = stubClient({
      searchKnowledgePages: vi.fn(async () => [
        {
          id: "p1",
          name: "Uploader guide",
          source_query: "How do uploads recover from failures?",
          snippet: "Uploads retry with backoff…",
          score: 0.031,
        },
        { id: "p2", name: "Auth notes", snippet: "Tokens rotate daily.", score: 0.012 },
      ]),
    });
    const tools = buildKnowledgeTools(client, "repo-a");
    const result = await findTool(tools, "wenyuan_search_knowledge_pages").handler({
      query: "upload retries",
    });
    expect(result.isError).toBeFalsy();
    // The tool passes no limit — the client's pageSearchLimit is the single source for it.
    expect(client.searchKnowledgePages).toHaveBeenCalledWith("upload retries");
    // No `score`: the server's rank-fusion number tops out near 0.03, so a model reading it
    // treats its best hit as 3% relevant. Rank order carries the ranking.
    const payload = JSON.parse(result.content[0]?.text as string);
    expect(payload.pages).toEqual([
      {
        page: "Uploader guide",
        page_id: "p1",
        description: "How do uploads recover from failures?",
        snippet: "Uploads retry with backoff…",
      },
      { page: "Auth notes", page_id: "p2", snippet: "Tokens rotate daily." },
    ]);
    // The credit reminder rides with the hits: the session guide has scrolled away by the time a
    // search lands mid-session, and a paraphrased snippet otherwise gets absorbed uncredited.
    expect(payload.crediting).toContain("From WenYuan memory");
    expect(payload.crediting).toContain("paraphrased");
  });

  it("wenyuan_search_knowledge_pages adds toolGuideExtra after the crediting note", async () => {
    const extra = "Memory is a past record: verify it against the code first.";
    const client = stubClient();
    const tool = findTool(
      buildKnowledgeTools(client, "repo-a", { toolGuideExtra: extra }),
      "wenyuan_search_knowledge_pages"
    );
    const { crediting } = JSON.parse(
      (await tool.handler({ query: "q" })).content[0]?.text as string
    );
    // Added, not replacing: the crediting rule is still there, and the team's text follows it.
    expect(crediting).toContain("From WenYuan memory");
    expect(crediting.endsWith(extra)).toBe(true);
    expect(tool.description.endsWith(extra)).toBe(true);
  });

  it("wenyuan_search_knowledge_pages returns isError:true when the server search throws", async () => {
    const client = stubClient({
      searchKnowledgePages: vi.fn(async () => {
        throw new Error("search down");
      }),
    });
    const result = await findTool(
      buildKnowledgeTools(client, "repo-a"),
      "wenyuan_search_knowledge_pages"
    ).handler({ query: "x" });
    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("search down");
  });

  it("wenyuan_list_knowledge_pages calls client.listPages() with no args", async () => {
    const client = stubClient({ listPages: vi.fn(async () => ({ pages: [{ id: "p1" }] })) });
    const tools = buildKnowledgeTools(client, "repo-a");
    const result = await findTool(tools, "wenyuan_list_knowledge_pages").handler({});
    expect(client.listPages).toHaveBeenCalledWith();
    expect(JSON.parse(result.content[0]?.text as string)).toEqual({ pages: [{ id: "p1" }] });
  });

  it("wenyuan_read_knowledge_page calls client.getPage(page_id)", async () => {
    const client = stubClient({ getPage: vi.fn(async (id: string) => ({ id, name: "X" })) });
    const tools = buildKnowledgeTools(client, "repo-a");
    const result = await findTool(tools, "wenyuan_read_knowledge_page").handler({ page_id: "p1" });
    expect(client.getPage).toHaveBeenCalledWith("p1");
    expect(JSON.parse(result.content[0]?.text as string)).toEqual({ id: "p1", name: "X" });
  });

  it("wenyuan_reflect calls client.reflect(query, {budget: high}) and returns the synthesis", async () => {
    const client = stubClient();
    const result = await findTool(buildKnowledgeTools(client, "repo-a"), "wenyuan_reflect").handler({
      query: "why is X 3?",
    });
    expect(result.isError).toBeFalsy();
    expect(client.reflect).toHaveBeenCalledWith("why is X 3?", {
      budget: "high",
      timeoutMs: DEFAULT_REFLECT_TOOL_TIMEOUT_MS,
    });
    expect(JSON.parse(result.content[0]?.text as string)).toBe("the decided rule is X=3");
  });

  it("wenyuan_reflect passes the configured timeout and budget through to the client", async () => {
    // The handler used to pass NO timeout, so the client fell back to a hardcoded 120s and aborted
    // every high-budget synthesis on a populated bank — with the configured value dead.
    const client = stubClient({
      reflect: vi.fn(async () => "answer"),
    });
    const tool = findTool(
      buildKnowledgeTools(client, "repo-a", { reflectTimeoutMs: 660_000, reflectBudget: "mid" }),
      "wenyuan_reflect"
    );
    await tool.handler({ query: "why?" });
    expect(client.reflect).toHaveBeenCalledWith("why?", { budget: "mid", timeoutMs: 660_000 });
  });

  it("wenyuan_reflect never leaves the timeout unset (the client default would abort at 120s)", async () => {
    const client = stubClient();
    await findTool(buildKnowledgeTools(client, "repo-a"), "wenyuan_reflect").handler({
      query: "why?",
    });
    const calls = (client.reflect as unknown as Mock).mock.calls;
    const opts = calls[0]?.[1] as { budget?: string; timeoutMs: number } | undefined;
    expect(opts?.timeoutMs).toBeGreaterThan(300_000); // above the server's own reflect wall timeout
  });

  it("wenyuan_capture_initiative calls client.captureInitiative({title, summary, relatesToPageId}) and returns the page id", async () => {
    const client = stubClient({
      captureInitiative: vi.fn(async (_a: unknown) => ({ page_id: "initiative-retry-backoff" })),
    });
    const tools = buildKnowledgeTools(client, "repo-a");
    const result = await findTool(tools, "wenyuan_capture_initiative").handler({
      title: "Retry backoff for the uploader",
      summary: "Add exponential backoff so transient upload failures self-heal.",
      relates_to_page_id: "initiative-uploader",
    });
    expect(client.captureInitiative).toHaveBeenCalledWith({
      title: "Retry backoff for the uploader",
      summary: "Add exponential backoff so transient upload failures self-heal.",
      relatesToPageId: "initiative-uploader",
    });
    expect(JSON.parse(result.content[0]?.text as string)).toEqual({
      page_id: "initiative-retry-backoff",
    });
  });

  // Regression guard for the "ONCE, EARLY" contract that told agents to capture the opening plan
  // and never recapture, so mid-work pivots never reached the initiative page.
  it("wenyuan_capture_initiative's description tells the agent to recapture when the plan changes", () => {
    const desc = findTool(
      buildKnowledgeTools(stubClient(), "repo-a"),
      "wenyuan_capture_initiative"
    ).description;
    expect(desc).not.toMatch(/ONCE, EARLY/i);
    expect(desc).toMatch(/call it AGAIN/i);
    expect(desc).toMatch(/goal, scope, or rationale/i);
    expect(desc).toMatch(/mid-implementation/i);
    // The recapture must reuse the existing page, not create a parallel one.
    expect(desc).toMatch(/relates_to_page_id = that/i);
    expect(desc).toMatch(/[Nn]ever mint a second page/);
    // Summary guidance must ask for the current intent, not the originally approved plan.
    expect(desc).toMatch(/CURRENT intent/);
    // Trivial course-corrections are still out of scope.
    expect(desc).toMatch(/trivial course-corrections/i);
  });

  it("wenyuan_capture_initiative passes relatesToPageId: undefined for a brand-new initiative", async () => {
    const client = stubClient();
    const tools = buildKnowledgeTools(client, "repo-a");
    await findTool(tools, "wenyuan_capture_initiative").handler({
      title: "New thing",
      summary: "why",
    });
    expect(client.captureInitiative).toHaveBeenCalledWith({
      title: "New thing",
      summary: "why",
      relatesToPageId: undefined,
    });
  });

  it("applies retain attribution to initiative capture and manual document ingestion", async () => {
    const client = stubClient();
    const stampFor = vi.fn(() => ({
      tags: ["project:repo-a"],
      metadata: { project: "repo-a" },
    }));
    const tools = buildKnowledgeTools(client, "repo-a", { harness: "codex", stampFor });

    await findTool(tools, "wenyuan_capture_initiative").handler({
      title: "New thing",
      summary: "why",
    });
    expect(client.captureInitiative).toHaveBeenCalledWith({
      title: "New thing",
      summary: "why",
      relatesToPageId: undefined,
      stamp: { tags: ["project:repo-a"], metadata: { project: "repo-a" } },
    });

    await findTool(tools, "wenyuan_ingest_document").handler({ title: "Notes", content: "x" });
    expect(client.retain).toHaveBeenCalledWith(
      "x",
      "ingested document",
      "notes",
      ["project:repo-a", "source:upload", "harness:codex"],
      "document",
      { metadata: { project: "repo-a", harness: "codex" } }
    );
    expect(stampFor).toHaveBeenCalledTimes(2);
  });

  it("wenyuan_ingest_document slugifies the title and calls client.retain(...) with the 'document' strategy", async () => {
    const client = stubClient();
    const tools = buildKnowledgeTools(client, "repo-a");
    const result = await findTool(tools, "wenyuan_ingest_document").handler({
      title: "My Title",
      content: "some content",
    });
    expect(client.retain).toHaveBeenCalledWith(
      "some content",
      "ingested document",
      "my-title",
      ["source:upload"],
      "document",
      {} // no harness in these tests: nothing to stamp
    );
    expect(JSON.parse(result.content[0]?.text as string)).toEqual({ ok: true, doc_id: "my-title" });
  });

  it("wenyuan_ingest_document collapses internal whitespace runs in the title into single hyphens", async () => {
    const client = stubClient();
    const tools = buildKnowledgeTools(client, "repo-a");
    await findTool(tools, "wenyuan_ingest_document").handler({
      title: "Repo   Core  Concepts",
      content: "x",
    });
    expect(client.retain).toHaveBeenCalledWith(
      "x",
      "ingested document",
      "repo-core-concepts",
      ["source:upload"],
      "document",
      {}
    );
  });

  it("wenyuan_ingest_document strips punctuation from the title into a safe slug", async () => {
    const client = stubClient();
    const tools = buildKnowledgeTools(client, "repo-a");
    await findTool(tools, "wenyuan_ingest_document").handler({
      title: "Repo: Component Map! (v2/final)",
      content: "x",
    });
    expect(client.retain).toHaveBeenCalledWith(
      "x",
      "ingested document",
      "repo-component-map-v2-final",
      ["source:upload"],
      "document",
      {}
    );
  });

  it.each([
    ["测试问题17记忆文档一", "测试问题17记忆文档二"],
    ["记忆文档一", "记忆文档二"],
    ["Résumé", "Rèsumé"],
    ["!!!///???", "???///!!!"],
  ])("wenyuan_ingest_document keeps distinct lossy titles: %s / %s", async (title, otherTitle) => {
    const documents = new Map<string, string>();
    const client = stubClient({
      retain: vi.fn(async (content: string, _context: string, documentId: string) => {
        documents.set(documentId, content);
      }),
    });
    const tool = findTool(buildKnowledgeTools(client, "repo-a"), "wenyuan_ingest_document");
    const first = JSON.parse(
      (await tool.handler({ title, content: "first document" })).content[0]?.text as string
    );
    const second = JSON.parse(
      (await tool.handler({ title: otherTitle, content: "second document" })).content[0]
        ?.text as string
    );
    expect(first.ok).toBe(true);
    expect(second.ok).toBe(true);
    expect(first.doc_id).not.toBe(second.doc_id);
    expect(documents.get(first.doc_id)).toBe("first document");
    expect(documents.get(second.doc_id)).toBe("second document");

    const updated = await tool.handler({ title, content: "updated first document" });
    expect(JSON.parse(updated.content[0]?.text as string)).toEqual(first);
    expect(documents.size).toBe(2);
    expect(documents.get(first.doc_id)).toBe("updated first document");
    expect(documents.get(second.doc_id)).toBe("second document");
  });

  for (const name of [
    "wenyuan_list_knowledge_pages",
    "wenyuan_read_knowledge_page",
    "wenyuan_reflect",
    "wenyuan_capture_initiative",
    "wenyuan_ingest_document",
  ] as const) {
    it(`${name} returns isError:true with the error text when the client method throws`, async () => {
      const boom = new Error("boom: not found");
      const client = stubClient({
        listPages: vi.fn(async () => {
          throw boom;
        }),
        getPage: vi.fn(async () => {
          throw boom;
        }),
        reflect: vi.fn(async () => {
          throw boom;
        }),
        captureInitiative: vi.fn(async () => {
          throw boom;
        }),
        retain: vi.fn(async () => {
          throw boom;
        }),
      });
      const tools = buildKnowledgeTools(client, "repo-a");
      const tool = findTool(tools, name);
      const args =
        name === "wenyuan_reflect"
          ? { query: "q" }
          : name === "wenyuan_capture_initiative"
            ? { title: "T", summary: "S" }
            : name === "wenyuan_ingest_document"
              ? { title: "T", content: "C" }
              : name === "wenyuan_read_knowledge_page"
                ? { page_id: "p1" }
                : {};
      const result = await tool.handler(args);
      expect(result.isError).toBe(true);
      expect(JSON.parse(result.content[0]?.text as string)).toEqual({ error: "boom: not found" });
    });
  }
});
