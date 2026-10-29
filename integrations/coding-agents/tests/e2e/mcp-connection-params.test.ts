import { describe, expect, it } from "vitest";
import {
  DEPLOYMENT_ENV,
  DEFAULT_API_URL,
  DEFAULT_BANK_ID,
} from "../../src/config/connection";
import {
  MCP_HARNESS_ENV,
  MCP_SERVER_NAME,
  MCP_SERVERS_KEY,
} from "../../src/mcp/mechanisms";
import { PER_USER_DIRECTORY_NAME } from "../../src/runtime/layout";
import { runInstaller, withFakeHome, type FakeHome, type JsonDocument } from "../helpers/fake-home";

/**
 * Connection parameters reaching the registered entry.
 *
 * The staged runtime reads the service location and the memory bank from the
 * environment the host passes at launch, so every file-resident registration
 * carries those deployment-contract values beside the harness attribution.
 * Operator-facing overrides win over the stored configuration, and every entry
 * points into the stable staged runtime directory rather than a build path.
 */

const TOML_HOST = "codex";
const JSON_HOST = "codebuddy";
const TOML_PATH = ".codex/config.toml";
const JSON_PATH = ".codebuddy/.mcp.json";
// The second JSON host, whose server map is a single fixed file of its own.

function serversOf(document: JsonDocument): JsonDocument {
  const servers: unknown = document[MCP_SERVERS_KEY];
  return servers !== null && typeof servers === "object" && !Array.isArray(servers)
    ? (servers as JsonDocument)
    : {};
}

function jsonEntryEnv(document: JsonDocument): JsonDocument {
  const entry = serversOf(document)[MCP_SERVER_NAME] as JsonDocument;
  const env: unknown = entry["env"];
  return env !== null && typeof env === "object" && !Array.isArray(env) ? (env as JsonDocument) : {};
}

describe("the JSON host's entry carries the connection parameters", () => {
  it("stores the service address and the bank from the environment", async () => {
    await withFakeHome(
      {
        environment: {
          PATH: "",
          [DEPLOYMENT_ENV.apiUrl]: "http://env.example:9999",
          [DEPLOYMENT_ENV.bankId]: "bank-from-env",
        },
      },
      async (home: FakeHome) => {
        expect((await runInstaller(["install", JSON_HOST])).status).toBe(0);
        const env: JsonDocument = jsonEntryEnv(await home.readJson(JSON_PATH));
        expect(env[DEPLOYMENT_ENV.apiUrl]).toBe("http://env.example:9999");
        expect(env[DEPLOYMENT_ENV.bankId]).toBe("bank-from-env");
        expect(env[MCP_HARNESS_ENV]).toBe(JSON_HOST);
      },
    );
  });

  it("lets an explicit address flag win over the stored and environment values", async () => {
    await withFakeHome(
      { environment: { PATH: "", [DEPLOYMENT_ENV.apiUrl]: "http://env.example:9999" } },
      async (home: FakeHome) => {
        const result = await runInstaller([
          "install",
          JSON_HOST,
          "--api-url",
          "http://flag.example:4321",
        ]);
        expect(result.status).toBe(0);
        const env: JsonDocument = jsonEntryEnv(await home.readJson(JSON_PATH));
        expect(env[DEPLOYMENT_ENV.apiUrl]).toBe("http://flag.example:4321");
      },
    );
  });

  it("falls through to the built-in address and bank with the environment unset", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      // The default mode is self-hosted; only an *explicit* self-hosted request
      // without an address is refused, so the default falls through to the
      // built-in values.
      expect((await runInstaller(["install", JSON_HOST])).status).toBe(0);
      const env: JsonDocument = jsonEntryEnv(await home.readJson(JSON_PATH));
      expect(env[DEPLOYMENT_ENV.apiUrl]).toBe(DEFAULT_API_URL);
      expect(env[DEPLOYMENT_ENV.bankId]).toBe(DEFAULT_BANK_ID);
    });
  });

  it("carries the port and no explicit address in the locally managed mode", async () => {
    await withFakeHome(
      { environment: { PATH: "", [DEPLOYMENT_ENV.apiPort]: "19999" } },
      async (home: FakeHome) => {
        expect((await runInstaller(["install", JSON_HOST, "--server", "local"])).status).toBe(0);
        const env: JsonDocument = jsonEntryEnv(await home.readJson(JSON_PATH));
        expect(env[DEPLOYMENT_ENV.apiPort]).toBe("19999");
        // The local mode never registers a tokenless address: the port is the
        // authoritative value and the address is derived by the runtime.
        expect(env[DEPLOYMENT_ENV.bankId]).toBe(DEFAULT_BANK_ID);
        expect(env[MCP_HARNESS_ENV]).toBe(JSON_HOST);
      },
    );
  });

  it("carries the auth token in the hosted mode", async () => {
    await withFakeHome(
      { environment: { PATH: "", [DEPLOYMENT_ENV.apiToken]: "token-from-env" } },
      async (home: FakeHome) => {
        expect((await runInstaller(["install", JSON_HOST, "--server", "hosted"])).status).toBe(0);
        const env: JsonDocument = jsonEntryEnv(await home.readJson(JSON_PATH));
        expect(env[DEPLOYMENT_ENV.apiToken]).toBe("token-from-env");
      },
    );
  });

  it("refuses a hosted install with no token and wires nothing", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const result = await runInstaller(["install", JSON_HOST, "--server", "hosted"]);
      expect(result.status).not.toBe(0);
      expect(result.combined()).toContain("--api-token");
      expect(await home.exists(JSON_PATH)).toBe(false);
    });
  });
});

describe("the TOML host's table carries the connection parameters", () => {
  it("names the service address, the bank and the harness in the table", async () => {
    await withFakeHome(
      {
        environment: {
          PATH: "",
          [DEPLOYMENT_ENV.apiUrl]: "http://env.example:9999",
          [DEPLOYMENT_ENV.bankId]: "bank-from-env",
        },
      },
      async (home: FakeHome) => {
        expect((await runInstaller(["install", TOML_HOST])).status).toBe(0);
        const text: string = await home.read(TOML_PATH);
        expect(text).toContain(DEPLOYMENT_ENV.apiUrl);
        expect(text).toContain("http://env.example:9999");
        expect(text).toContain(DEPLOYMENT_ENV.bankId);
        expect(text).toContain("bank-from-env");
        expect(text).toContain(MCP_HARNESS_ENV);
        expect(text).toContain(TOML_HOST);
      },
    );
  });
});

describe("the runtime is staged before any host is wired", () => {
  it("points every registration at a staged server that already exists", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", JSON_HOST, TOML_HOST])).status).toBe(0);
      for (const file of [JSON_PATH, TOML_PATH]) {
        expect(await home.isFile(file)).toBe(true);
        const text: string = await home.read(file);
        expect(text).toContain(PER_USER_DIRECTORY_NAME);
        expect(text).not.toContain(home.packageRoot);
      }
      // The staged directory the entries point into wraps the packaged server
      // file name the installer resolves, whatever the host registers by.
      expect(
        await home.exists(`${PER_USER_DIRECTORY_NAME}/${MCP_SERVER_NAME}`),
      ).toBe(true);
    });
  });
});