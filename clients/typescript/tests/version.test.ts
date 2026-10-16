import { describe, expect, it } from "vitest";

import { WenYuanClient } from "../src/index";
import { call, stubFetch } from "./helpers";

describe("getVersion", () => {
  it("calls the version endpoint through the configured client", async () => {
    const stub = stubFetch(() => ({
      json: {
        api_version: "0.8.2",
        features: { observations: true, mcp: true, bank_config: true },
      },
    }));
    const client = new WenYuanClient({ baseUrl: "http://api.test", fetch: stub.fetch });

    const version = await client.getVersion();

    expect(version.api_version).toBe("0.8.2");
    expect(version.features.observations).toBe(true);
    expect(stub.calls).toHaveLength(1);
    expect(call(stub).method).toBe("GET");
    expect(call(stub).url).toBe("http://api.test/version");
  });

  it("raises the server's error as a typed throw", async () => {
    const stub = stubFetch(() => ({ status: 500, json: { detail: "boom" } }));
    const client = new WenYuanClient({ baseUrl: "http://api.test", fetch: stub.fetch });

    await expect(client.getVersion()).rejects.toThrow(/getVersion failed/);
  });
});