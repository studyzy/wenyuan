/**
 * End-to-end tests against a running WenYuan server.
 *
 * The whole file is skipped unless `HINDSIGHT_API_URL` is set, so the default
 * `npm test` run stays offline. Point it at a server started with
 * `HINDSIGHT_API_DATABASE_URL=memory` — no model provider — and the assertions
 * stay structural: an operation succeeded, an id round-tripped, a list carries
 * what was written. Nothing here depends on extracted facts or synthesised
 * prose.
 *
 *   HINDSIGHT_API_URL=http://127.0.0.1:18888 npm test
 */
import { afterAll, describe, expect, it } from "vitest";
import { inflateRawSync } from "node:zlib";

import {
  WenYuanClient,
  WenYuanError,
  createClient,
  recall,
  reflect,
  retain,
  retainBatch,
  sdk,
} from "../src/index";
import type { Client } from "../src/index";

const API_URL = process.env.HINDSIGHT_API_URL;

/** A live server needs real time: reflect may wait on a model call. */
const TIMEOUT = 120_000;

/**
 * Read one entry out of a ZIP archive by name.
 *
 * The end-of-central-directory record points at the central directory, which
 * carries each entry's sizes and where its bytes start — the one place a ZIP
 * reader can rely on, since a streaming writer defers the sizes in the local
 * headers to a data descriptor.
 */
function readZipEntry(zip: Uint8Array, entryName: string): Uint8Array {
  const view = new DataView(zip.buffer, zip.byteOffset, zip.byteLength);
  let eocd = -1;
  for (let i = zip.length - 22; i >= 0; i--) {
    if (view.getUint32(i, true) === 0x06054b50) {
      eocd = i;
      break;
    }
  }
  if (eocd < 0) throw new Error("the archive has no end-of-central-directory record");
  const entries = view.getUint16(eocd + 10, true);
  let offset = view.getUint32(eocd + 16, true);
  for (let i = 0; i < entries; i++) {
    if (view.getUint32(offset, true) !== 0x02014b50) {
      throw new Error(`no central-directory header at ${offset}`);
    }
    const method = view.getUint16(offset + 10, true);
    const compressedSize = view.getUint32(offset + 20, true);
    const nameLength = view.getUint16(offset + 28, true);
    const extraLength = view.getUint16(offset + 30, true);
    const commentLength = view.getUint16(offset + 32, true);
    const localOffset = view.getUint32(offset + 42, true);
    const name = new TextDecoder().decode(zip.subarray(offset + 46, offset + 46 + nameLength));
    if (name === entryName) {
      const start =
        localOffset +
        30 +
        view.getUint16(localOffset + 26, true) +
        view.getUint16(localOffset + 28, true);
      const data = zip.subarray(start, start + compressedSize);
      if (method === 0) return data;
      if (method === 8) return new Uint8Array(inflateRawSync(data));
      throw new Error(`unsupported ZIP compression method ${method}`);
    }
    offset += 46 + nameLength + extraLength + commentLength;
  }
  throw new Error(`the archive carries no ${entryName} entry`);
}

describe.skipIf(!API_URL)("WenYuan live API", () => {
  const client: Client = createClient({ baseUrl: API_URL });
  const created: string[] = [];

  afterAll(async () => {
    for (const bankId of created) {
      await sdk.deleteBank({ client, path: { bank_id: bankId } });
    }
  });

  /** Create a bank the test owns, so a run against a shared server leaves nothing behind. */
  async function newBank(mission?: string): Promise<string> {
    const bankId = `ts-sdk-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
    const { response } = await sdk.createOrUpdateBank({
      client,
      path: { bank_id: bankId },
      body: mission === undefined ? {} : { reflect_mission: mission },
    });
    expect(response?.status).toBe(200);
    created.push(bankId);
    return bankId;
  }

  it("runs the bank lifecycle: create, list, read config, delete", async () => {
    const bankId = await newBank("remember the lifecycle");

    const listed = await sdk.listBanks({ client });
    expect(listed.response?.status).toBe(200);
    const found = listed.data?.banks.find((bank) => bank.bank_id === bankId);
    expect(found).toBeDefined();

    const config = await sdk.getBankConfig({ client, path: { bank_id: bankId } });
    expect(config.response?.status).toBe(200);
    expect(config.data?.config["reflect_mission"]).toBe("remember the lifecycle");

    const deleted = await sdk.deleteBank({ client, path: { bank_id: bankId } });
    expect(deleted.response?.status).toBe(200);
    created.splice(created.indexOf(bankId), 1);

    const after = await sdk.listBanks({ client });
    expect(after.data?.banks.some((bank) => bank.bank_id === bankId)).toBe(false);
  }, TIMEOUT);

  it("reads back a document and its chunks after a retain", async () => {
    const bankId = await newBank();
    await retain(client, bankId, "The schema stays the reference's schema, object for object.");

    const documents = await sdk.listDocuments({ client, path: { bank_id: bankId } });
    expect(documents.response?.status).toBe(200);
    expect(documents.data?.items.length ?? 0).toBeGreaterThan(0);
    const documentId = documents.data?.items[0]?.id;
    expect(documentId).toBeTruthy();

    const chunks = await sdk.listDocumentChunks({
      client,
      path: { bank_id: bankId, document_id: documentId! },
    });
    expect(chunks.response?.status).toBe(200);
    expect(chunks.data?.items.length ?? 0).toBeGreaterThan(0);
    const chunkId = chunks.data?.items[0]?.chunk_id;
    expect(chunkId).toBeTruthy();

    const chunk = await sdk.getChunk({ client, path: { chunk_id: chunkId! } });
    expect(chunk.response?.status).toBe(200);
    expect(chunk.data?.chunk_text.trim().length ?? 0).toBeGreaterThan(0);
  }, TIMEOUT);

  it("selects a tagged write back out and finds nothing for an unwritten tag", async () => {
    const bankId = await newBank();
    const tag = "ts-sdk-tagged";

    const response = await retainBatch(
      client,
      bankId,
      [
        {
          content:
            "The first tagged note records that Alice reviews the schema before every release.",
          tags: [tag],
        },
        {
          content:
            "The second tagged note records that Bob keeps the migration series generated.",
          tags: [tag],
        },
      ],
      { document_tags: ["ts-sdk-document"] },
    );
    expect(response.items_count).toBe(2);

    // The query shares terms with the content on purpose: a live assertion on a
    // recall result has to be one the deployment's own text search can satisfy,
    // and the phrasing differs between backends (a lexical score in a
    // memory-backed deployment, a stemmed text search in a PostgreSQL one).
    const tagged = await recall(client, bankId, "what do the tagged notes record?", {
      tags: [tag],
    });
    expect(tagged.results.length).toBeGreaterThan(0);

    // A tag nobody wrote must select nothing, which proves the filter is the tag
    // and not "any query returns the bank's content".
    const unwritten = await recall(client, bankId, "what do the tagged notes record?", {
      tags: ["ts-sdk-never-written"],
    });
    expect(unwritten.results).toHaveLength(0);
  }, TIMEOUT);

  it("names an operation for an async retain and queues it", async () => {
    const bankId = await newBank();
    const response = await retain(client, bankId, "queued work", { async: true });

    expect(response.async).toBe(true);
    const operationId = response.operation_id;
    expect(operationId).toBeTruthy();

    const operations = await sdk.listOperations({ client, path: { bank_id: bankId } });
    expect(operations.response?.status).toBe(200);
    const queued = operations.data?.operations.some(
      (operation) => operation.operation_id === operationId || operation.id === operationId,
    );
    expect(queued).toBe(true);

    const status = await sdk.getOperationStatus({
      client,
      path: { bank_id: bankId, operation_id: operationId! },
    });
    expect(status.response?.status).toBe(200);
    expect(status.data?.status).toBeTruthy();
  }, TIMEOUT);

  it("finds a just-retained memory with recall", async () => {
    const bankId = await newBank();
    await retain(client, bankId, "Alice prefers tea over coffee in the afternoon.");

    const response = await recall(client, bankId, "what does Alice prefer to drink?");
    expect(response.results.length).toBeGreaterThan(0);
    for (const result of response.results) {
      expect(result.id).toBeTruthy();
      expect(result.text.trim().length).toBeGreaterThan(0);
    }
  }, TIMEOUT);

  it("synthesises an answer with reflect", async () => {
    const bankId = await newBank();
    await retain(client, bankId, "The migration series is generated from the reference catalogue.");

    try {
      const response = await reflect(client, bankId, "how is the migration series produced?");
      expect(response.text.trim().length).toBeGreaterThan(0);
    } catch (error) {
      // A deployment may have a model configured and that model may be
      // unreachable or slow; reflection then answers its documented
      // wall-clock-budget refusal. The call reached the handler and the client
      // decoded the refusal, which is what this suite proves.
      if (error instanceof WenYuanError && error.statusCode === 504) return;
      throw error;
    }
  }, TIMEOUT);

  it("answers the retired profile route with 410", async () => {
    const bankId = await newBank();

    // The contract retires the profile read, so the 410 is what makes
    // GET .../config the supported way to read a bank's settings.
    const { response } = await sdk.getBankProfile({ client, path: { bank_id: bankId } });
    expect(response?.status).toBe(410);
  }, TIMEOUT);

  /** The convenience client, against the same server. */
  const api = new WenYuanClient({ baseUrl: API_URL });

  /** Create a bank through the convenience client, tracked for cleanup. */
  async function newBankViaClient(mission?: string): Promise<string> {
    const bankId = `ts-sdk-cv-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
    const profile = await api.createBank(
      bankId,
      mission === undefined ? {} : { reflectMission: mission },
    );
    expect(profile.bank_id).toBe(bankId);
    created.push(bankId);
    return bankId;
  }

  it("round-trips a bank's config, mission and delete through the convenience client", async () => {
    const bankId = await newBankViaClient("remember the convenience client");

    const config = await api.getBankConfig(bankId);
    expect(config.config["reflect_mission"]).toBe("remember the convenience client");

    // A narrowed update reaches the stored override: the resolved read reflects
    // it whichever of the two maps the server reports it in.
    await api.updateBankConfig(bankId, { retainChunkSize: 2048 });
    const updated = await api.getBankConfig(bankId);
    expect({ ...updated.config, ...updated.overrides }["retain_chunk_size"]).toBe(2048);

    await api.resetBankConfig(bankId);
    const reset = await api.getBankConfig(bankId);
    expect({ ...reset.config, ...reset.overrides }["retain_chunk_size"]).not.toBe(2048);

    await api.setMission(bankId, "a replacement mission");
    const remissed = await api.getBankConfig(bankId);
    expect(remissed.config["reflect_mission"]).toBe("a replacement mission");

    await api.deleteBank(bankId);
    await expect(api.getBankConfig(bankId)).rejects.toThrow();
  }, TIMEOUT);

  it("creates and lists a directive", async () => {
    const bankId = await newBankViaClient();

    const created = await api.createDirective(bankId, "ts-sdk-rule", "Answer in one sentence.", {
      tags: ["ts-sdk"],
      priority: 1,
    });
    expect(created.id).toBeTruthy();

    const listed = await api.listDirectives(bankId);
    expect(listed.items.map((item) => item.id)).toContain(created.id);

    const fetched = await api.getDirective(bankId, created.id);
    expect(fetched.name).toBe("ts-sdk-rule");

    await api.deleteDirective(bankId, created.id);
    const after = await api.listDirectives(bankId);
    expect(after.items.map((item) => item.id)).not.toContain(created.id);
  }, TIMEOUT);

  it("creates and lists a mental model", async () => {
    const bankId = await newBankViaClient();

    const created = await api.createMentalModel(bankId, "ts-sdk-model", "What do we know?", {
      tags: ["ts-sdk"],
    });
    expect(created.mental_model_id).toBeTruthy();

    const listed = await api.listMentalModels(bankId, { detail: "metadata", limit: 50 });
    expect(listed.items.map((item) => item.id)).toContain(created.mental_model_id);
  }, TIMEOUT);

  it("exports a bank as a ZIP archive whose manifest names it", async () => {
    const bankId = await newBankViaClient();
    await api.retain(bankId, "The transfer round trip writes exactly one document.");

    const bytes = await api.exportBank(bankId, {
      includeData: true,
      includeBankConfig: true,
      includeHistory: false,
      pollIntervalMs: 200,
      timeoutMs: 30_000,
    });

    // A ZIP local file header, and a manifest that names the source bank.
    expect(Array.from(bytes.slice(0, 4))).toEqual([0x50, 0x4b, 0x03, 0x04]);
    const manifest = JSON.parse(
      new TextDecoder().decode(readZipEntry(bytes, "manifest.json")),
    ) as { source_bank_id?: string; archive_type?: string; document_count?: number };
    expect(manifest.source_bank_id).toBe(bankId);
    expect(manifest.archive_type).toBeTruthy();
    expect(manifest.document_count).toBeGreaterThan(0);
  }, TIMEOUT);

  it("imports an exported archive into a fresh bank", async () => {
    const source = await newBankViaClient();
    await api.retain(source, "The import round trip writes exactly one document.");
    const bytes = await api.exportBank(source, {
      includeData: true,
      includeBankConfig: true,
      pollIntervalMs: 200,
      timeoutMs: 30_000,
    });

    const target = `ts-sdk-import-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
    created.push(target);
    const operationId = await api.importBank(
      source,
      new File([bytes.slice()], "transfer.zip", { type: "application/zip" }),
      { targetBankId: target },
    );
    expect(operationId).toBeTruthy();

    // The restore runs in the background; the target bank existing is the
    // structural proof that it did.
    const deadline = Date.now() + 30_000;
    let exists = false;
    while (!exists && Date.now() < deadline) {
      const status = await sdk.getOperationStatus({
        client,
        path: { bank_id: source, operation_id: operationId },
      });
      if (status.data?.status === "failed" || status.data?.status === "cancelled") {
        throw new Error(`import ${status.data.status}: ${status.data.error_message ?? ""}`);
      }
      const banks = await sdk.listBanks({ client });
      exists = banks.data?.banks.some((bank) => bank.bank_id === target) ?? false;
      // Real time on purpose: this waits on another process's background
      // restore, so there is no in-process signal to await instead.
      if (!exists) await new Promise((resolve) => setTimeout(resolve, 200));
    }
    expect(exists).toBe(true);
  }, TIMEOUT);
});