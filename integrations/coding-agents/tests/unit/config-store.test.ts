import { readFile, stat } from "node:fs/promises";
import { describe, expect, it } from "vitest";
import {
  DEFAULT_API_URL,
  DEPLOYMENT_ENV,
  SERVER_MODES,
  resolveConnection,
  runtimeEnvironment,
} from "../../src/config/connection";
import { defaultStoredConfigPath, homeDirectory, storedConfigPath } from "../../src/config/paths";
import { readStoredConfig, writeStoredConfig } from "../../src/config/store";
import { withFakeHome } from "../helpers/fake-home";

/**
 * The stored configuration and the fixed precedence chain, tested against the
 * file on disk: flag, then stored value, then environment override, then the
 * built-in default.
 */

describe("the stored configuration file", () => {
  it("lives under the home directory by default", async () => {
    await withFakeHome({}, async (home) => {
      expect(homeDirectory()).toBe(home.home);
      expect(storedConfigPath()).toBe(defaultStoredConfigPath());
      expect(storedConfigPath().startsWith(home.home)).toBe(true);
    });
  });

  it("merges into what is already there, keeping unrelated keys", async () => {
    await withFakeHome({}, async (home) => {
      await home.write(".wenyuan/config.json", '{\n  "theme": "dark"\n}\n');
      await writeStoredConfig({ bankId: "team-a" });
      const document = await readStoredConfig();
      expect(document.theme).toBe("dark");
      expect(document.bankId).toBe("team-a");
    });
  });

  it("removes a key whose value is undefined rather than writing null", async () => {
    await withFakeHome({}, async (home) => {
      await home.write(".wenyuan/config.json", '{\n  "apiUrl": "http://old"\n}\n');
      await writeStoredConfig({ apiUrl: undefined });
      expect(await readStoredConfig()).toEqual({});
    });
  });

  it("is written owner-only", async () => {
    await withFakeHome({}, async () => {
      await writeStoredConfig({ serverMode: "self-hosted" });
      if (process.platform !== "win32") {
        expect((await stat(storedConfigPath())).mode & 0o077).toBe(0);
      }
    });
  });

  it("honors the relocation variable and leaves the default path alone", async () => {
    await withFakeHome({}, async (home) => {
      process.env.WENYUAN_CONFIG = home.path("relocated", "config.json");
      await writeStoredConfig({ bankId: "relocated" });
      expect(await home.exists("relocated/config.json")).toBe(true);
      expect(await home.exists(".wenyuan/config.json")).toBe(false);
    });
  });
});

describe("validating before anything is written", () => {
  it("refuses an unrecognized server mode and names the accepted values", async () => {
    await withFakeHome({}, async (home) => {
      const result = await resolveConnection({ server: "cloud" });
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.message).toContain("cloud");
        for (const mode of SERVER_MODES) {
          expect(result.message).toContain(mode);
        }
      }
      expect(await home.exists(".wenyuan/config.json")).toBe(false);
    });
  });

  it("refuses the self-hosted mode without an address and names the flag", async () => {
    await withFakeHome({}, async (home) => {
      const result = await resolveConnection({ server: "self-hosted" });
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.message).toContain("--api-url");
      }
      expect(await home.exists(".wenyuan/config.json")).toBe(false);
    });
  });

  it("refuses the hosted mode without a token and names the flag", async () => {
    await withFakeHome({}, async (home) => {
      const result = await resolveConnection({ server: "hosted" });
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.message).toContain("--api-token");
      }
      expect(await home.exists(".wenyuan/config.json")).toBe(false);
    });
  });
});

describe("the precedence chain", () => {
  it("leaves an already-stored mode and address exactly as they are", async () => {
    await withFakeHome({}, async (home) => {
      await home.write(
        ".wenyuan/config.json",
        '{\n  "serverMode": "self-hosted",\n  "apiUrl": "http://stored:9000"\n}\n',
      );
      const before: string = await home.read(".wenyuan/config.json");
      const result = await resolveConnection();
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.connection.serverMode).toBe("self-hosted");
        expect(result.connection.apiUrl).toBe("http://stored:9000");
      }
      expect(await home.read(".wenyuan/config.json")).toBe(before);
    });
  });

  it("lets a flag override a stored value and keeps unrelated keys", async () => {
    await withFakeHome({}, async (home) => {
      await home.write(
        ".wenyuan/config.json",
        '{\n  "apiUrl": "http://stored:9000",\n  "theme": "dark"\n}\n',
      );
      const result = await resolveConnection({ apiUrl: "http://flag:9100" });
      expect(result.ok).toBe(true);
      const document = await readStoredConfig();
      expect(document.apiUrl).toBe("http://flag:9100");
      expect(document.theme).toBe("dark");
    });
  });

  it("takes the address from the environment, then from the built-in default", async () => {
    await withFakeHome(
      { environment: { WENYUAN_API_URL: "http://env:9200" } },
      async () => {
        const fromEnvironment = await resolveConnection();
        expect(fromEnvironment.ok).toBe(true);
        if (fromEnvironment.ok) {
          expect(fromEnvironment.connection.apiUrl).toBe("http://env:9200");
        }
        expect((await readStoredConfig()).apiUrl).toBe("http://env:9200");
      },
    );
    await withFakeHome({}, async () => {
      const fromDefault = await resolveConnection();
      expect(fromDefault.ok).toBe(true);
      if (fromDefault.ok) {
        expect(fromDefault.connection.apiUrl).toBe(DEFAULT_API_URL);
      }
    });
  });

  it("takes the bank identifier from the environment, then from the built-in default", async () => {
    await withFakeHome(
      { environment: { WENYUAN_BANK_ID: "team-b" } },
      async () => {
        const result = await resolveConnection();
        expect(result.ok).toBe(true);
        if (result.ok) {
          expect(result.connection.bankId).toBe("team-b");
        }
      },
    );
    await withFakeHome({}, async () => {
      const result = await resolveConnection();
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.connection.bankId).toBe("default");
      }
    });
  });

  it("takes the hosted credential from the environment and writes it under its key", async () => {
    await withFakeHome(
      { environment: { WENYUAN_API_TOKEN: "secret-token" } },
      async () => {
        const result = await resolveConnection({ server: "hosted" });
        expect(result.ok).toBe(true);
        if (result.ok) {
          expect(result.connection.apiToken).toBe("secret-token");
        }
        expect((await readStoredConfig()).apiToken).toBe("secret-token");
      },
    );
  });

  it("records the port and no explicit address in the locally managed mode", async () => {
    await withFakeHome(
      { environment: { WENYUAN_API_PORT: "19999" } },
      async () => {
        const result = await resolveConnection({ server: "local" });
        expect(result.ok).toBe(true);
        if (result.ok) {
          expect(result.connection.serverMode).toBe("local");
          expect(result.connection.apiPort).toBe(19999);
          expect(result.connection.apiUrl).toContain("19999");
        }
        const document = await readStoredConfig();
        expect(document.apiPort).toBe(19999);
        expect(document.apiUrl).toBeUndefined();
      },
    );
  });

  it("reports missing local prerequisites as advisory warnings and still completes", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async () => {
      const result = await resolveConnection({ server: "local" });
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.connection.warnings.length).toBeGreaterThan(0);
      }
    });
  });

  it("spells the deployment environment names in the block the runtime reads", async () => {
    await withFakeHome({ environment: { WENYUAN_BANK_ID: "team-d" } }, async () => {
      const result = await resolveConnection({ server: "self-hosted", apiUrl: "http://x:1" });
      expect(result.ok).toBe(true);
      if (result.ok) {
        const environment = runtimeEnvironment(result.connection);
        expect(environment.WENYUAN_BANK_ID).toBe("team-d");
        expect(environment.WENYUAN_API_URL).toBe("http://x:1");
        expect(environment.WENYUAN_API_TOKEN).toBeUndefined();
      }
    });
  });

  it("names the deployment environment variables it honors", () => {
    expect(DEPLOYMENT_ENV.apiUrl).toBe("WENYUAN_API_URL");
    expect(DEPLOYMENT_ENV.apiToken).toBe("WENYUAN_API_TOKEN");
    expect(DEPLOYMENT_ENV.bankId).toBe("WENYUAN_BANK_ID");
    expect(DEPLOYMENT_ENV.apiPort).toBe("WENYUAN_API_PORT");
  });
});

describe("reading the stored configuration back", () => {
  it("reads a broken file as empty rather than failing", async () => {
    await withFakeHome({}, async (home) => {
      await home.write(".wenyuan/config.json", "{ not json");
      expect(await readStoredConfig()).toEqual({});
    });
  });

  it("keeps the file's own trailing newline convention", async () => {
    await withFakeHome({}, async () => {
      await writeStoredConfig({ bankId: "team-c" });
      expect(await readFile(storedConfigPath(), "utf8")).toMatch(/\n$/);
    });
  });
});
