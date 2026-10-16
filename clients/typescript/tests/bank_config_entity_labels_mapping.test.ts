/**
 * The entity-labels request mapping on updateBankConfig.
 *
 * updateBankConfig enumerates config fields rather than passing a dict through,
 * so a field the wrapper does not know about is silently dropped for every
 * consumer of the SDK, with no type error to catch it. `entityLabels` is exactly
 * that kind of gap: without the option, TypeScript callers cannot configure a
 * controlled vocabulary at all.
 */
import { describe, expect, it } from "vitest";

import { WenYuanClient } from "../src/index";
import type { LabelGroupInput } from "../src/index";
import { call, jsonBody, jsonObject, stubFetch, type Stub } from "./helpers";

function client() {
  const stub = stubFetch(() => ({ json: { bank_id: "bank", config: {}, overrides: {} } }));
  return {
    stub,
    client: new WenYuanClient({ baseUrl: "http://api.test", fetch: stub.fetch }),
  };
}

/** The `updates` map of the first (only) config call. */
function sentUpdates(stub: Stub): Record<string, unknown> {
  return jsonObject(jsonBody(call(stub)).updates, "updates");
}

describe("entity labels mapping", () => {
  it("maps entity labels onto the updates map", async () => {
    const { client: api, stub } = client();
    const entityLabels: LabelGroupInput[] = [
      {
        key: "name",
        type: "multi-text",
        tag: true,
        description: "Every name the subject of this fact is known by.",
      },
      { key: "topic", type: "value", values: [{ value: "infra" }] },
    ];

    await api.updateBankConfig("bank", { entityLabels, entitiesAllowFreeForm: false });

    expect(sentUpdates(stub)).toEqual({
      entity_labels: entityLabels,
      entities_allow_free_form: false,
    });
  });

  it("omits entity labels when unset, so the vocabulary survives", async () => {
    const { client: api, stub } = client();

    await api.updateBankConfig("bank", { retainChunkSize: 1000 });

    const updates = sentUpdates(stub);
    expect(updates.entity_labels).toBeUndefined();
    expect(updates.entities_allow_free_form).toBeUndefined();
    expect(updates).toEqual({ retain_chunk_size: 1000 });
  });
});