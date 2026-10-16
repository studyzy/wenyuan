/**
 * The wrapper forwards multimodal content blocks unaltered.
 *
 * `content` accepts `string | ContentBlock[]` so an image can sit inline where it
 * appears. The wrapper is what most consumers call, so a wrapper that coerced or
 * dropped the list form would strip images for every TypeScript user while the
 * generated SDK happily supported them.
 */
import { describe, expect, it } from "vitest";

import { WenYuanClient } from "../src/index";
import type { ContentBlock } from "../src/index";
import { call, jsonBody, jsonObject, stubFetch, type Stub } from "./helpers";

const IMAGE_BLOCK: ContentBlock = {
  type: "image",
  source: { type: "base64", media_type: "image/png", data: "aGVsbG8=" },
};

function client() {
  const stub = stubFetch(() => ({ json: { success: true, bank_id: "test-bank", items_count: 1 } }));
  return {
    stub,
    client: new WenYuanClient({ baseUrl: "http://api.test", fetch: stub.fetch }),
  };
}

/** The retained items of the first (only) call. */
function sentItems(stub: Stub): unknown[] {
  const items = jsonBody(call(stub)).items;
  if (!Array.isArray(items)) throw new Error("the retain request carried no items");
  return items;
}

function firstItem(stub: Stub): Record<string, unknown> {
  return jsonObject(sentItems(stub)[0], "item");
}

describe("retain content blocks", () => {
  it("forwards a block list verbatim", async () => {
    const { client: api, stub } = client();
    const content: ContentBlock[] = [
      { type: "text", text: "click the button shown:" },
      IMAGE_BLOCK,
      { type: "text", text: "...then reconnect." },
    ];

    await api.retain("test-bank", content);

    expect(firstItem(stub).content).toEqual(content);
  });

  it("still forwards a plain string", async () => {
    const { client: api, stub } = client();

    await api.retain("test-bank", "Alice joined the AI team");

    expect(firstItem(stub).content).toBe("Alice joined the AI team");
  });

  it("forwards block lists through retainBatch", async () => {
    const { client: api, stub } = client();

    await api.retainBatch("test-bank", [
      { content: [{ type: "text", text: "see:" }, IMAGE_BLOCK] },
      { content: "plain text item" },
    ]);

    expect(jsonObject(sentItems(stub)[0], "item").content).toEqual([
      { type: "text", text: "see:" },
      IMAGE_BLOCK,
    ]);
    expect(jsonObject(sentItems(stub)[1], "item").content).toBe("plain text item");
  });

  it("serialises a Date timestamp and applies the batch documentId", async () => {
    const { client: api, stub } = client();
    const when = new Date("2024-03-01T09:00:00.000Z");

    await api.retainBatch(
      "test-bank",
      [{ content: "first", timestamp: when }, { content: "second", document_id: "own-doc" }],
      { documentId: "batch-doc" },
    );

    expect(firstItem(stub).timestamp).toBe("2024-03-01T09:00:00.000Z");
    expect(firstItem(stub).document_id).toBe("batch-doc");
    expect(jsonObject(sentItems(stub)[1], "item").document_id).toBe("own-doc");
  });
});