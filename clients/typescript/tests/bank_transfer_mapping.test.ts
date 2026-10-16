/**
 * The bank export/import wrappers' request mapping and transfer flow.
 *
 * These need no server: the generated sdk's HTTP calls are recorded, so the
 * camelCase options can be checked against the snake_case query the API reads —
 * and an omitted flag is genuinely absent rather than sent as undefined, because
 * the server's default for each of the three scope flags decides how much of a
 * bank travels.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { WenYuanClient, WenYuanError } from "../src/index";
import { call, stubFetch, type Stub } from "./helpers";

const OPERATION_ID = "029110c8-a2b2-464c-a206-52c99b76cbf1";
const DOWNLOAD_URL = "/v1/default/files/download/banks/my-bank/exports/x/transfer.zip";
const SIGNED_URL = "https://storage.example.com/archive?X-Amz-Signature=abc%2Fdef+ghi";
const ARCHIVE = new Uint8Array([0x50, 0x4b, 0x03, 0x04]);

/** A client whose transfer calls reach the completed operation immediately. */
function transferClient(options: { downloadUrl?: string; status?: unknown } = {}) {
  const stub = stubFetch((request) => {
    if (
      request.pathname.endsWith("/export") ||
      request.pathname.endsWith("/clone") ||
      request.pathname.endsWith("/transfer/import")
    ) {
      return { status: 202, json: { operation_id: OPERATION_ID } };
    }
    if (request.pathname.includes("/operations/")) {
      return {
        json: options.status ?? {
          status: "completed",
          result_metadata: { download_url: options.downloadUrl ?? DOWNLOAD_URL },
        },
      };
    }
    return { bytes: ARCHIVE };
  });
  return {
    stub,
    client: new WenYuanClient({
      baseUrl: "http://api.test",
      apiKey: "secret",
      fetch: stub.fetch,
    }),
  };
}

/** The polling requests among the recorded calls. */
function polls(stub: Stub): number {
  return stub.calls.filter((request) => request.path.includes("/operations/")).length;
}

describe("exportBank mapping", () => {
  it("submits, polls and downloads the archive the operation names", async () => {
    const { client: api, stub } = transferClient();

    const bytes = await api.exportBank("my-bank", { pollIntervalMs: 0 });

    expect(Array.from(bytes)).toEqual(Array.from(ARCHIVE));
    expect(call(stub, 0).path).toBe("/v1/default/banks/my-bank/transfer/export");
    expect(call(stub, 0).method).toBe("POST");
    expect(call(stub, 1).path).toBe(`/v1/default/banks/my-bank/operations/${OPERATION_ID}`);
    expect(call(stub, 2).url).toBe(`http://api.test${DOWNLOAD_URL}`);
  });

  it("keeps an API-relative download on the authenticated client", async () => {
    const { client: api, stub } = transferClient();

    await api.exportBank("my-bank", { pollIntervalMs: 0 });

    expect(call(stub, 2).headers["authorization"]).toBe("Bearer secret");
  });

  it("does not send omitted scope flags, so the server's defaults decide", async () => {
    const { client: api, stub } = transferClient();

    await api.exportBank("my-bank", { pollIntervalMs: 0 });

    expect([...call(stub, 0).query.keys()]).toEqual([]);
  });

  it("forwards a narrowed scope snake_cased", async () => {
    const { client: api, stub } = transferClient();

    await api.exportBank("my-bank", {
      includeData: true,
      includeBankConfig: false,
      includeHistory: true,
      pollIntervalMs: 0,
    });

    expect(call(stub, 0).query.get("include_data")).toBe("true");
    expect(call(stub, 0).query.get("include_bank_config")).toBe("false");
    expect(call(stub, 0).query.get("include_history")).toBe("true");
  });

  it("throws on a failed export rather than returning empty bytes", async () => {
    const { client: api } = transferClient({
      status: { status: "failed", error_message: "disk full" },
    });

    await expect(api.exportBank("my-bank", { pollIntervalMs: 0 })).rejects.toThrow("disk full");
  });

  it("throws when the operation completes without a download_url", async () => {
    const { client: api } = transferClient({ status: { status: "completed" } });

    await expect(api.exportBank("my-bank", { pollIntervalMs: 0 })).rejects.toThrow(
      /without a download_url/,
    );
  });

  it("maps the document export's own query", async () => {
    const { client: api, stub } = transferClient();

    await api.exportDocuments("my-bank", {
      documentIds: ["doc-1", "doc-2"],
      includeObservations: true,
      includeKnowledgeBase: false,
      pollIntervalMs: 0,
    });

    expect(call(stub, 0).pathname).toBe("/v1/default/banks/my-bank/document-transfer/export");
    expect(call(stub, 0).query.getAll("document_id")).toEqual(["doc-1", "doc-2"]);
    expect(call(stub, 0).query.get("include_observations")).toBe("true");
    expect(call(stub, 0).query.get("include_knowledge_base")).toBe("false");
  });
});

describe("signed export archive downloads", () => {
  afterEach(() => vi.unstubAllGlobals());

  it.each(["exportBank", "exportDocuments"] as const)(
    "%s uses the signed URL without forwarding API credentials",
    async (method) => {
      const signedFetch = vi.fn(async () => new Response(ARCHIVE));
      vi.stubGlobal("fetch", signedFetch);
      const { client: api, stub } = transferClient({ downloadUrl: SIGNED_URL });

      const bytes = await api[method]("my-bank", { pollIntervalMs: 0 });

      expect(Array.from(bytes)).toEqual(Array.from(ARCHIVE));
      // The storage host gets the URL and nothing else: no Authorization
      // header, no base URL, no wrapper of its own.
      expect(signedFetch).toHaveBeenCalledWith(SIGNED_URL, { signal: undefined });
      expect(polls(stub)).toBe(1);
    },
  );
});

describe("export timeoutMs bounds the whole polling phase", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  /** Let the submit and status calls resolve until the poll's deadline timer is armed. */
  async function untilTimerArmed(): Promise<void> {
    for (let i = 0; i < 100 && vi.getTimerCount() === 0; i++) {
      await vi.advanceTimersByTimeAsync(0);
    }
    if (vi.getTimerCount() === 0) throw new Error("the poll never armed a timer");
  }

  /** The error a rejected export settles with. */
  async function failure(pending: Promise<Uint8Array>): Promise<WenYuanError> {
    const outcome = await pending.then(
      () => new Error("the export resolved instead of failing"),
      (error: unknown) => error,
    );
    if (!(outcome instanceof WenYuanError)) {
      throw new Error(`expected a WenYuanError, got ${String(outcome)}`);
    }
    return outcome;
  }

  it.each(["exportBank", "exportDocuments"] as const)(
    "%s does not wait for a status request that answers after the deadline",
    async (method) => {
      const stub = stubFetch((request) =>
        request.path.includes("/export")
          ? { status: 202, json: { operation_id: OPERATION_ID } }
          : // The status request never answers; only the deadline can end the wait.
            new Promise<never>(() => {}),
      );
      const client = new WenYuanClient({ baseUrl: "http://api.test", fetch: stub.fetch });

      const pending = client[method]("my-bank", { pollIntervalMs: 0, timeoutMs: 50 });
      // Track the outcome before the clock moves: the export rejects on a timer,
      // and an unhandled rejection is not what this test is about.
      const settled = failure(pending);
      await untilTimerArmed();
      await vi.advanceTimersByTimeAsync(50);

      expect((await settled).message).toContain("did not complete within 50ms");
      // The status request went out, and the download never did.
      expect(stub.calls).toHaveLength(2);
    },
  );

  it.each(["exportBank", "exportDocuments"] as const)(
    "%s cuts a poll interval longer than the budget short, without another poll",
    async (method) => {
      const stub = stubFetch((request) =>
        request.path.includes("/export")
          ? { status: 202, json: { operation_id: OPERATION_ID } }
          : { json: { status: "processing" } },
      );
      const client = new WenYuanClient({ baseUrl: "http://api.test", fetch: stub.fetch });

      const pending = client[method]("my-bank", { pollIntervalMs: 5000, timeoutMs: 50 });
      const settled = failure(pending);
      await untilTimerArmed();
      await vi.advanceTimersByTimeAsync(50);

      expect((await settled).message).toContain("did not complete within 50ms");
      expect(polls(stub)).toBe(1);
    },
  );
});

describe("importBank mapping", () => {
  it("sends the archive as the request body and returns the operation id", async () => {
    const { client: api, stub } = transferClient();
    const archive = new File([ARCHIVE], "transfer.zip", { type: "application/zip" });

    const operationId = await api.importBank("my-bank", archive, {
      targetBankId: "my-bank-copy",
    });

    expect(operationId).toBe(OPERATION_ID);
    expect(call(stub).path).toBe("/v1/default/banks/my-bank/transfer/import?target_bank_id=my-bank-copy");
    expect(call(stub).method).toBe("POST");
    expect(call(stub).headers["content-type"]).toContain("multipart/form-data");
    expect(call(stub).text).toContain('name="file"');
    expect(call(stub).text).toContain("transfer.zip");
  });

  it("restores a subset when asked, and sends nothing when not", async () => {
    const { client: api, stub } = transferClient();
    const archive = new File([ARCHIVE], "transfer.zip", { type: "application/zip" });

    await api.importBank("my-bank", archive, { includeBankConfig: false });
    expect(call(stub, 0).query.get("include_bank_config")).toBe("false");

    await api.importBank("my-bank", archive);
    expect(call(stub, 1).query.get("include_bank_config")).toBeNull();
    expect([...call(stub, 1).query.keys()]).toEqual([]);
  });
});

describe("cloneBank mapping", () => {
  it("names the source in the path and the target in the query", async () => {
    const { client: api, stub } = transferClient();

    const operationId = await api.cloneBank("my-bank", "my-bank-copy");

    expect(operationId).toBe(OPERATION_ID);
    expect(call(stub).path).toBe("/v1/default/banks/my-bank/clone?target_bank_id=my-bank-copy");
  });

  it("forwards a narrowed scope, and sends nothing when not asked", async () => {
    const { client: api, stub } = transferClient();

    await api.cloneBank("my-bank", "my-bank-copy", { includeBankConfig: false });
    expect(call(stub, 0).query.get("include_bank_config")).toBe("false");

    await api.cloneBank("my-bank", "my-bank-copy");
    expect(call(stub, 1).query.get("include_bank_config")).toBeNull();
  });
});