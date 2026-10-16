/**
 * `retain`/`retainBatch` forward resolve_entities into the request body.
 *
 * The wrapper builds each item field by field, so a new field is dropped unless
 * it is added here too. Dropping this one would silently restore the entity
 * substitution the flag exists to prevent.
 */
import { describe, expect, it } from "vitest";

import { WenYuanClient } from "../src/index";
import { call, jsonBody, jsonObject, stubFetch, type Stub } from "./helpers";

function client() {
  const stub = stubFetch(() => ({ json: { success: true, bank_id: "bank", items_count: 1 } }));
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

describe("retain resolve_entities", () => {
  it("forwards resolveEntities false", async () => {
    const { client: api, stub } = client();

    await api.retain("bank", "The patient saw a specialist.", {
      entities: [{ text: "Dr. Waller", type: "PERSON" }],
      resolveEntities: false,
    });

    const item = jsonObject(sentItems(stub)[0], "item");
    expect(item.resolve_entities).toBe(false);
    expect(item.entities).toEqual([{ text: "Dr. Waller", type: "PERSON" }]);
  });

  it("omitting resolveEntities leaves the server default in place", async () => {
    const { client: api, stub } = client();

    await api.retain("bank", "A fact.", { entities: [{ text: "Alice" }] });

    expect("resolve_entities" in jsonObject(sentItems(stub)[0], "item")).toBe(false);
  });

  it("passes a per-item resolve_entities through retainBatch", async () => {
    const { client: api, stub } = client();

    await api.retainBatch("bank", [
      { content: "A fact.", entities: [{ text: "Alice" }], resolve_entities: false },
    ]);

    expect(jsonObject(sentItems(stub)[0], "item").resolve_entities).toBe(false);
  });

  it("warns when an operationId cannot be honoured on a synchronous retain", async () => {
    const { client: api, stub } = client();
    const warnings: string[] = [];
    const spy = (message: string) => warnings.push(message);
    const previous = console.warn;
    console.warn = spy;
    try {
      await api.retain("bank", "A fact.", { operationId: "op-1" });
    } finally {
      console.warn = previous;
    }

    expect(warnings).toHaveLength(1);
    expect(warnings[0]).toContain("operationId is ignored for synchronous retain");
    expect("operation_id" in jsonBody(call(stub))).toBe(false);
  });

  it("sends the operationId for an asynchronous retain", async () => {
    const { client: api, stub } = client();

    await api.retain("bank", "A fact.", { async: true, operationId: "op-1" });

    expect(jsonBody(call(stub)).operation_id).toBe("op-1");
  });
});