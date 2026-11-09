import { afterEach, describe, expect, it, vi } from "vitest";
import * as listRoute from "../app/api/list/route";
import * as documents from "../app/api/documents/route";
import * as entities from "../app/api/entities/route";
import * as graph from "../app/api/graph/route";
import * as retain from "../app/api/memories/retain/route";
import * as retainAsync from "../app/api/memories/retain_async/route";
import * as memory from "../app/api/memories/[memoryId]/route";
import * as operations from "../app/api/operations/[agentId]/route";
import * as recall from "../app/api/recall/route";
import * as reflect from "../app/api/reflect/route";
import { stubFetch, withEnv } from "./support/fixtures";
import { expectNoErrorKey, makeRequest, readJson } from "./support/harness";
import { TENANT_SESSION_COOKIE, createTenantSessionToken } from "../lib/server/session";

let restore: (() => void) | undefined;
afterEach(() => {
  restore?.();
  restore = undefined;
  delete process.env.WENYUAN_CP_DATAPLANE_API_URL;
});

describe("memory-list delete is unsupported", () => {
  it("returns 501 with a localized message", async () => {
    const response = await listRoute.DELETE(makeRequest("http://localhost/api/list?bank_id=acme"));
    expect(response.status).toBe(501);
    const body = await readJson(response);
    expectNoErrorKey(body);
    expect((body.error ?? body.message) as string).not.toBe("errors.unsupportedMemoriesDelete");
  });
});

describe("required query parameters", () => {
  it("returns 400 when document_id is missing", async () => {
    const response = await documents.GET(makeRequest("http://localhost/api/documents"));
    expect(response.status).toBe(400);
    expectNoErrorKey(await readJson(response));
  });

  it("returns 400 when bank_id is missing", async () => {
    const response = await listRoute.GET(makeRequest("http://localhost/api/list"));
    expect(response.status).toBe(400);
    expectNoErrorKey(await readJson(response));
  });

  it("requires bank_id on the entity collection and does not accept the agent-style alias there", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const accepted = await entities.GET(
        makeRequest("http://localhost/api/entities?bank_id=acme"),
      );
      expect(accepted.status).toBe(200);

      const rejected = await entities.GET(
        makeRequest("http://localhost/api/entities?agent_id=acme"),
      );
      expect(rejected.status).toBe(400);
      expectNoErrorKey(await readJson(rejected));
    });
  });
});

describe("memory route method sets", () => {
  it("binds retain and retain_async as POST", () => {
    expect(typeof retain.POST).toBe("function");
    expect(typeof retainAsync.POST).toBe("function");
  });

  it("binds per-memory read/update", () => {
    expect(typeof memory.GET).toBe("function");
    expect(typeof memory.PATCH).toBe("function");
  });

  it("binds operations get/delete", () => {
    expect(typeof operations.GET).toBe("function");
    expect(typeof operations.DELETE).toBe("function");
    expect(Object.keys(operations).sort()).toEqual(["DELETE", "GET"]);
  });

  it("binds recall, reflect, documents, entities and graph", () => {
    expect(typeof recall.POST).toBe("function");
    expect(typeof reflect.POST).toBe("function");
    expect(typeof documents.GET).toBe("function");
    expect(typeof entities.GET).toBe("function");
    expect(typeof graph.GET).toBe("function");
  });
});

describe("recall and reflect take the bank from the request body", () => {
  it("routes recall to the bank named in the body and normalises a legacy fact type", async () => {
    const stub = stubFetch([{ status: 200, body: { memories: [] } }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await recall.POST(
        makeRequest("http://localhost/api/recall", {
          method: "POST",
          body: JSON.stringify({ bank_id: "team/alpha", query: "q", fact_type: "world" }),
        }),
      );

      expect(response.status).toBe(200);
      expect(stub.calls[0]).toContain("/banks/team%2Falpha/memories/recall");
      const forwarded = JSON.parse(stub.bodies[0] ?? "{}");
      expect(forwarded.types).toBe("world");
      expect(forwarded).not.toHaveProperty("fact_type");
      // The budget always reaches the retrieval arms, so an unset one is mid.
      expect(forwarded.budget).toBe("mid");
    });
  });

  it("falls back to the default bank when recall names none", async () => {
    const stub = stubFetch([{ status: 200, body: { memories: [] } }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await recall.POST(
        makeRequest("http://localhost/api/recall", {
          method: "POST",
          body: JSON.stringify({ query: "q" }),
        }),
      );

      expect(response.status).toBe(200);
      expect(stub.calls[0]).toContain("/banks/default/memories/recall");
    });
  });

  it("recalls with the credential of the tenant the console entered", async () => {
    const stub = stubFetch([{ status: 200, body: { results: [] } }]);
    restore = stub.restore;
    await withEnv(
      {
        WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local",
        WENYUAN_CP_DATAPLANE_API_KEY: "static-key",
        WENYUAN_CP_ADMIN_API_KEY: "operator-key",
      },
      async () => {
        const cookie =
          TENANT_SESSION_COOKIE +
          "=" +
          createTenantSessionToken("operator-key", "studyzy", "wyk_tenant", Date.now());
        const response = await recall.POST(
          makeRequest("http://localhost/api/recall", {
            method: "POST",
            cookie,
            body: JSON.stringify({ bank_id: "studyzy::WeKnora", query: "q" }),
          }),
        );

        expect(response.status).toBe(200);
        const headers = new Headers(stub.headers[0]);
        expect(headers.get("authorization")).toBe("Bearer wyk_tenant");
      },
    );
  });

  it("passes an upstream refusal through instead of reporting a server fault", async () => {
    const stub = stubFetch([{ status: 401, body: { detail: "Authentication failed" } }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await recall.POST(
        makeRequest("http://localhost/api/recall", {
          method: "POST",
          body: JSON.stringify({ bank_id: "acme", query: "q" }),
        }),
      );

      expect(response.status).toBe(401);
      expect((await readJson(response)).details).toBe("Authentication failed");
    });
  });

  it("routes reflect to the bank named in the body and keeps the id out of the upstream body", async () => {
    const stub = stubFetch([{ status: 200, body: { answer: "ok" } }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await reflect.POST(
        makeRequest("http://localhost/api/reflect", {
          method: "POST",
          body: JSON.stringify({ bank_id: "acme", query: "q" }),
        }),
      );

      expect(response.status).toBe(200);
      expect(stub.calls[0]).toContain("/banks/acme/reflect");
      expect(JSON.parse(stub.bodies[0] ?? "{}")).not.toHaveProperty("bank_id");
    });
  });

  it("falls back to the default bank when reflect names none", async () => {
    const stub = stubFetch([{ status: 200, body: { answer: "ok" } }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await reflect.POST(
        makeRequest("http://localhost/api/reflect", {
          method: "POST",
          body: JSON.stringify({ query: "q" }),
        }),
      );

      expect(response.status).toBe(200);
      expect(stub.calls[0]).toContain("/banks/default/reflect");
    });
  });

  it("lets a reflect that outruns the shared 15s default complete", async () => {
    // The stub answers after 60s — past the shared default, inside reflect's.
    const original = globalThis.fetch;
    globalThis.fetch = (async (_input: RequestInfo | URL, init?: RequestInit) =>
      await new Promise<Response>((resolve, reject) => {
        const timer = setTimeout(
          () => resolve(new Response(JSON.stringify({ answer: "ok" }), { status: 200 })),
          60_000,
        );
        init?.signal?.addEventListener("abort", () => {
          clearTimeout(timer);
          reject(new DOMException("aborted", "AbortError"));
        });
      })) as typeof fetch;
    vi.useFakeTimers();
    try {
      await withEnv(
        { WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" },
        async () => {
          const pending = reflect.POST(
            makeRequest("http://localhost/api/reflect", {
              method: "POST",
              body: JSON.stringify({ bank_id: "acme", query: "q" }),
            }),
          );

          // Past the shared 15s default, short of reflect's 180s bound: only a
          // route that raised its deadline can still succeed here.
          await vi.advanceTimersByTimeAsync(60_000);
          expect((await pending).status).toBe(200);
        },
      );
    } finally {
      vi.useRealTimers();
      globalThis.fetch = original;
    }
  });
});