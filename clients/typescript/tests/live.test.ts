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

import {
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
});