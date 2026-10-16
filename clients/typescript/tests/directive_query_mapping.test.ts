/**
 * Directive query forwarding in the hand-written wrapper.
 *
 * The directive list endpoint reports a `total`, so a regression that dropped
 * `limit`/`offset` here would strand callers on the server's first page.
 */
import { describe, expect, it } from "vitest";

import { WenYuanClient } from "../src/index";
import { call, stubFetch } from "./helpers";

function client() {
  const stub = stubFetch(() => ({ json: { items: [], total: 0 } }));
  return {
    stub,
    client: new WenYuanClient({ baseUrl: "http://api.test", fetch: stub.fetch }),
  };
}

describe("directive query mapping", () => {
  it("forwards every supported list query option", async () => {
    const { client: api, stub } = client();
    const controller = new AbortController();

    await api.listDirectives("bank-1", {
      tags: ["project"],
      limit: 25,
      offset: 50,
      signal: controller.signal,
    });

    expect(call(stub).method).toBe("GET");
    expect(call(stub).path).toBe(
      "/v1/default/banks/bank-1/directives?tags=project&limit=25&offset=50",
    );
  });

  it("preserves the optionless and tags-only request shapes", async () => {
    const { client: api, stub } = client();

    await api.listDirectives("bank-1");
    expect([...call(stub, 0).query.keys()]).toEqual([]);

    await api.listDirectives("bank-1", { tags: ["project"] });
    expect(call(stub, 1).query.getAll("tags")).toEqual(["project"]);
  });

  it("creates a directive with the documented defaults", async () => {
    const stub = stubFetch(() => ({ json: { id: "d-1", name: "rule", content: "text" } }));
    const client = new WenYuanClient({ baseUrl: "http://api.test", fetch: stub.fetch });

    await client.createDirective("bank-1", "rule", "text");

    expect(call(stub).method).toBe("POST");
    expect(call(stub).body).toEqual({
      name: "rule",
      content: "text",
      priority: 0,
      is_active: true,
    });
  });

  it("updates only the fields the caller named", async () => {
    const stub = stubFetch(() => ({ json: { id: "d-1" } }));
    const client = new WenYuanClient({ baseUrl: "http://api.test", fetch: stub.fetch });

    await client.updateDirective("bank-1", "d-1", { isActive: false });

    // Undefined keys are dropped in transit, so what the server reads is exactly
    // the one field the caller set.
    expect(call(stub).body).toEqual({ is_active: false });
    expect(call(stub).path).toBe("/v1/default/banks/bank-1/directives/d-1");
  });
});