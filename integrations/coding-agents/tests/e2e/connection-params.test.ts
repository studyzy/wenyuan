import { describe, expect, it } from "vitest";
import { DEFAULT_API_URL, DEFAULT_BANK_ID, SERVER_MODES, CONFIG_KEYS } from "../../src/config/connection";
import { runInstaller, withFakeHome, type FakeHome, type JsonDocument } from "../helpers/fake-home";

/**
 * Connection parameters: the fixed precedence chain and its validation.
 *
 * Each parameter resolves from an explicit flag, then the stored configuration,
 * then the deployment environment override, then the built-in default. An
 * already-stored service mode or address short-circuits the chain entirely, and
 * every refusal happens before anything is written.
 */

const STORED = ".wenyuan/config.json";
const CONFIG = ".claude/settings.json";

async function nothingWasWritten(home: FakeHome): Promise<void> {
  expect(await home.exists(STORED)).toBe(false);
  expect(await home.exists(CONFIG)).toBe(false);
}

describe("the self-hosted mode without a service address", () => {
  it("names the address flag and writes nothing", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const result = await runInstaller(["install", "claude-code", "--server", "self-hosted"]);
      expect(result.status).not.toBe(0);
      expect(result.combined()).toContain("--api-url");
      await nothingWasWritten(home);
    });
  });

  it("is refused even when an address is only available by default", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const result = await runInstaller(["install", "claude-code", "--server=self-hosted"]);
      expect(result.status).not.toBe(0);
      // No silent fallback to another server: the default address is neither
      // adopted nor printed as the resolved one.
      expect(result.combined()).not.toContain(DEFAULT_API_URL);
      await nothingWasWritten(home);
    });
  });
});

describe("the hosted mode without an auth token", () => {
  it("names the token flag and writes nothing", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const result = await runInstaller(["install", "claude-code", "--server", "hosted"]);
      expect(result.status).not.toBe(0);
      expect(result.combined()).toContain("--api-token");
      await nothingWasWritten(home);
    });
  });
});

describe("an unrecognized server mode", () => {
  it("names the accepted values and writes nothing", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const result = await runInstaller(["install", "claude-code", "--server", "cloud"]);
      expect(result.status).not.toBe(0);
      expect(result.combined()).toContain("cloud");
      for (const mode of SERVER_MODES) {
        expect(result.combined()).toContain(mode);
      }
      await nothingWasWritten(home);
    });
  });
});

describe("a stored configuration that already names a mode and an address", () => {
  it("is left exactly as it is and the host is wired anyway", async () => {
    const stored = `${JSON.stringify({ serverMode: "self-hosted", apiUrl: "http://stored.example:1234" }, null, 2)}\n`;
    await withFakeHome({ files: { [STORED]: stored }, environment: { PATH: "" } }, async (home: FakeHome) => {
      const result = await runInstaller(["install", "claude-code"]);
      expect(result.status).toBe(0);
      expect(await home.read(STORED)).toBe(stored);
      expect(await home.isFile(CONFIG)).toBe(true);
      expect(result.combined()).not.toContain("[y/N]");
    });
  });
});

describe("a flag over an already-stored value", () => {
  it("wins and leaves every unrelated key alone", async () => {
    const stored = `${JSON.stringify({ apiUrl: "http://stored.example:1234", theme: "dark" }, null, 2)}\n`;
    await withFakeHome({ files: { [STORED]: stored }, environment: { PATH: "" } }, async (home: FakeHome) => {
      const result = await runInstaller(["install", "claude-code", "--api-url", "http://flag.example:4321"]);
      expect(result.status).toBe(0);
      const written: JsonDocument = await home.readJson(STORED);
      expect(written[CONFIG_KEYS.apiUrl]).toBe("http://flag.example:4321");
      expect(written["theme"]).toBe("dark");
    });
  });
});

describe("the server-mode flag", () => {
  it.each([
    ["the space-separated spelling", ["--server", "local"]],
    ["the equals-joined spelling", ["--server=local"]],
  ])("persists the requested mode in %s", async (_name: string, flag: string[]) => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const result = await runInstaller(["install", "claude-code", ...flag]);
      expect(result.status).toBe(0);
      expect(result.combined()).not.toContain("unknown host");
      expect((await home.readJson(STORED))[CONFIG_KEYS.serverMode]).toBe("local");
    });
  });
});

describe("the relocation variable", () => {
  it("moves the stored configuration and leaves the default path alone", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      // The relocated path lives inside the fake home, so it can only be named
      // once the fixture exists.
      const relocated: string = home.path("elsewhere", "config.json");
      process.env.WENYUAN_CONFIG = relocated;

      const result = await runInstaller(["install", "claude-code", "--server", "local"]);
      expect(result.status).toBe(0);
      expect(await home.exists("elsewhere/config.json")).toBe(true);
      expect((await home.readJson("elsewhere/config.json"))[CONFIG_KEYS.serverMode]).toBe("local");
      expect(await home.exists(STORED)).toBe(false);
      expect(await home.isFile(CONFIG)).toBe(true);
    });
  });
});

describe("the service address", () => {
  it("comes from the environment when nothing else names it", async () => {
    await withFakeHome(
      { environment: { PATH: "", WENYUAN_API_URL: "http://env.example:9999" } },
      async (home: FakeHome) => {
        expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
        expect((await home.readJson(STORED))[CONFIG_KEYS.apiUrl]).toBe("http://env.example:9999");
      },
    );
  });

  it("falls through to the built-in default with the environment unset", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
      expect((await home.readJson(STORED))[CONFIG_KEYS.apiUrl]).toBe(DEFAULT_API_URL);
    });
  });
});

describe("the auth token", () => {
  it("is taken from the environment and stored under its key", async () => {
    await withFakeHome(
      { environment: { PATH: "", WENYUAN_API_TOKEN: "token-from-env" } },
      async (home: FakeHome) => {
        const result = await runInstaller(["install", "claude-code", "--server", "hosted"]);
        expect(result.status).toBe(0);
        expect((await home.readJson(STORED))[CONFIG_KEYS.apiToken]).toBe("token-from-env");
        expect(await home.isFile(CONFIG)).toBe(true);
      },
    );
  });

  it("is refused with the environment unset", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const result = await runInstaller(["install", "claude-code", "--server", "hosted"]);
      expect(result.status).not.toBe(0);
      expect(result.combined()).toContain("--api-token");
      await nothingWasWritten(home);
    });
  });
});

describe("the bank identifier", () => {
  it.each([
    ["the environment", { WENYUAN_BANK_ID: "bank-from-env" }, "bank-from-env"],
    ["the built-in default", {}, DEFAULT_BANK_ID],
  ])("comes from %s", async (_name: string, extra: Record<string, string>, expected: string) => {
    await withFakeHome({ environment: { PATH: "", ...extra } }, async (home: FakeHome) => {
      const result = await runInstaller(["install", "claude-code"]);
      expect(result.status).toBe(0);
      expect(result.combined()).not.toContain("[y/N]");
      expect((await home.readJson(STORED))[CONFIG_KEYS.bankId]).toBe(expected);
      expect(await home.isFile(CONFIG)).toBe(true);
    });
  });
});

describe("the locally managed mode", () => {
  it("stores the port, no explicit address, and warns about missing prerequisites", async () => {
    await withFakeHome(
      { environment: { PATH: "", WENYUAN_API_PORT: "19999" } },
      async (home: FakeHome) => {
        const result = await runInstaller(["install", "claude-code", "--server", "local"]);
        expect(result.status).toBe(0);
        const written: JsonDocument = await home.readJson(STORED);
        expect(written[CONFIG_KEYS.apiPort]).toBe(19999);
        expect(written[CONFIG_KEYS.apiUrl]).toBeUndefined();
        expect(result.combined()).toContain("warning:");
        expect(await home.isFile(CONFIG)).toBe(true);
      },
    );
  });
});
