import { describe, expect, it } from "vitest";
import { GET as health } from "../app/api/health/route";
import { GET as version } from "../app/api/version/route";
import { stubFetch, withEnv } from "./support/fixtures";
import { makeRequest, readJson } from "./support/harness";

describe("health route", () => {
  it("reports a connected dataplane when the upstream is reachable", async () => {
    const stub = stubFetch([{ status: 200, body: { banks: [] } }]);
    const restore = stub.restore;
    try {
      await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
        const response = await health(makeRequest("http://localhost/api/health"));
        expect(response.status).toBe(200);
        const body = await readJson(response);
        expect(body.service).toContain("control-plane");
        expect(body.status).toBe("ok");
        const dataplane = body.dataplane as Record<string, unknown>;
        expect(dataplane.status).toBe("connected");
        expect(dataplane.url).toContain("http://upstream.local");
      });
    } finally {
      restore();
    }
  });

  it("reports a disconnected dataplane with an error string on failure", async () => {
    const stub = stubFetch([{ status: 503, body: { error: "down" } }]);
    const restore = stub.restore;
    try {
      await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
        const response = await health(makeRequest("http://localhost/api/health"));
        expect(response.status).toBe(200);
        const body = await readJson(response);
        expect(body.status).toBe("degraded");
        const dataplane = body.dataplane as Record<string, unknown>;
        expect(dataplane.status).toBe("disconnected");
        expect(typeof dataplane.error).toBe("string");
      });
    } finally {
      restore();
    }
  });
});

describe("version route", () => {
  it("injects the access-key-auth flag when the key is set", async () => {
    const stub = stubFetch([{ status: 200, body: { version: "1.0.0", features: {} } }]);
    const restore = stub.restore;
    try {
      await withEnv(
        { HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local", HINDSIGHT_CP_ACCESS_KEY: "secret" },
        async () => {
          const response = await version(makeRequest("http://localhost/api/version"));
          expect(response.status).toBe(200);
          const body = await readJson(response);
          const features = body.features as Record<string, boolean>;
          expect(features.access_key_auth).toBe(true);
        },
      );
    } finally {
      restore();
    }
  });

  it("sets the flag false when the key is unset", async () => {
    const stub = stubFetch([{ status: 200, body: { version: "1.0.0", features: {} } }]);
    const restore = stub.restore;
    try {
      await withEnv(
        { HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local", HINDSIGHT_CP_ACCESS_KEY: undefined },
        async () => {
          const response = await version(makeRequest("http://localhost/api/version"));
          const body = await readJson(response);
          const features = body.features as Record<string, boolean>;
          expect(features.access_key_auth).toBe(false);
        },
      );
    } finally {
      restore();
    }
  });

  it("returns a localized 500 when the upstream version probe fails", async () => {
    const stub = stubFetch([{ status: 500, body: { error: "boom" } }]);
    const restore = stub.restore;
    try {
      await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
        const response = await version(makeRequest("http://localhost/api/version"));
        expect(response.status).toBe(500);
        const body = await readJson(response);
        expect(JSON.stringify(body)).not.toContain("__errorKey");
      });
    } finally {
      restore();
    }
  });
});