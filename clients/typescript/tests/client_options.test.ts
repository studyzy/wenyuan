import { describe, expect, it } from "vitest";

import { DEFAULT_USER_AGENT, WenYuanClient } from "../src/index";
import { call, stubFetch } from "./helpers";

/** A client whose every call succeeds, so the request it makes can be inspected. */
function clientWithVersion(options: ConstructorParameters<typeof WenYuanClient>[0] = {}) {
  const stub = stubFetch(() => ({ json: { api_version: "1.0.0" } }));
  const client = new WenYuanClient({
    baseUrl: "http://api.test",
    fetch: stub.fetch,
    ...options,
  });
  return { client, stub };
}

describe("WenYuanClient options", () => {
  it("carries custom headers, the bearer key and the client's own user agent", async () => {
    const { client, stub } = clientWithVersion({
      apiKey: "test-api-key",
      headers: { "X-Request-Source": "test-suite" },
    });

    await client.getVersion();

    expect(call(stub).headers["x-request-source"]).toBe("test-suite");
    expect(call(stub).headers["authorization"]).toBe("Bearer test-api-key");
    expect(call(stub).headers["user-agent"]).toBe(DEFAULT_USER_AGENT);
  });

  it("puts a caller's user agent in front of the client's own", async () => {
    const { client, stub } = clientWithVersion({ userAgent: "acme-test/1.2.3" });

    await client.getVersion();

    expect(call(stub).headers["user-agent"]).toBe(`acme-test/1.2.3 ${DEFAULT_USER_AGENT}`);
  });

  it("sends no Authorization header when no key is configured", async () => {
    const { client, stub } = clientWithVersion();

    await client.getVersion();

    expect(call(stub).headers["authorization"]).toBeUndefined();
  });

  it("falls back to the deployment's environment, like the free helpers", async () => {
    const previous = process.env.WENYUAN_API_URL;
    process.env.WENYUAN_API_URL = "http://env.test:1234";
    try {
      const stub = stubFetch(() => ({ json: { api_version: "1.0.0" } }));
      const client = new WenYuanClient({ fetch: stub.fetch });

      await client.getVersion();

      expect(call(stub).url).toBe("http://env.test:1234/version");
    } finally {
      if (previous === undefined) delete process.env.WENYUAN_API_URL;
      else process.env.WENYUAN_API_URL = previous;
    }
  });
});