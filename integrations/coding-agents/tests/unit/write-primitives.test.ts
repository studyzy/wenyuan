import { readFile, stat, writeFile } from "node:fs/promises";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  asJsonDocument,
  containerAt,
  documentIsEmpty,
  mergeEntryIntoContainer,
  pruneEmptyContainers,
  readJsonFile,
  removeOwnEntriesFromContainer,
  seedSchemaVersion,
  writeJsonFile,
} from "../../src/write/json-merge";
import { backupExistingFile, backupPathFor } from "../../src/write/backup";
import { OWNERSHIP_MARKER, valueCarriesMarker } from "../../src/write/marker";
import { createFakeHome, type FakeHome } from "../helpers/fake-home";

/**
 * The write primitives are tested against files on disk: what a broken read
 * yields, what a merge leaves behind, and what a backup captures.
 */

const ownEntry = { command: `/home/tester/.wenyuan/${OWNERSHIP_MARKER}/hooks/stop.mjs` };
const foreignEntry = { command: "/usr/local/bin/other-tool" };

let home: FakeHome;
let restore: () => void;

beforeEach(async () => {
  home = await createFakeHome();
  restore = home.apply();
});

afterEach(async () => {
  restore();
  await home.cleanup();
});

describe("the defensive read", () => {
  const cases: { name: string; content: string | null; expected: unknown }[] = [
    { name: "a missing file", content: null, expected: {} },
    { name: "an unparseable file", content: "{ not json", expected: {} },
    { name: "an array top level", content: "[1, 2]", expected: {} },
    { name: "a null top level", content: "null", expected: {} },
    { name: "a scalar top level", content: '"just a string"', expected: {} },
    { name: "an object top level", content: '{"model":"sonnet"}', expected: { model: "sonnet" } },
  ];

  it.each(cases)("reads $name as the right document", async ({ content, expected }) => {
    const path: string = home.path("host", "settings.json");
    if (content !== null) {
      await home.write("host/settings.json", content);
    }
    const read = await readJsonFile(path);
    expect(read.document).toEqual(expected);
    expect(read.existed).toBe(content !== null);
  });

  it("keeps parse results consistent with the on-disk read", () => {
    expect(asJsonDocument("[]")).toEqual({});
    expect(asJsonDocument('{"a":1}')).toEqual({ a: 1 });
  });
});

describe("merging rather than replacing", () => {
  it("leaves unrelated keys and foreign entries in place and adds exactly one of its own", () => {
    const document = {
      model: "sonnet",
      hooks: { "session-start": [foreignEntry] },
    };
    mergeEntryIntoContainer(document, ["hooks", "session-start"], ownEntry);
    expect(document.model).toBe("sonnet");
    expect(containerAt(document, ["hooks", "session-start"])).toEqual([foreignEntry, ownEntry]);
  });

  it("is idempotent: a second merge leaves exactly one entry of its own", () => {
    const document: Record<string, unknown> = {};
    mergeEntryIntoContainer(document, ["hooks", "stop"], ownEntry);
    mergeEntryIntoContainer(document, ["hooks", "stop"], ownEntry);
    expect(containerAt(document, ["hooks", "stop"])).toEqual([ownEntry]);
  });

  it("creates the containers an entry needs", () => {
    const document: Record<string, unknown> = {};
    mergeEntryIntoContainer(document, ["hooks", "pre-prompt"], ownEntry);
    expect(document).toEqual({ hooks: { "pre-prompt": [ownEntry] } });
  });

  it("keeps several foreign entries in order", () => {
    const document: Record<string, unknown> = { hooks: { stop: [{ command: "a" }, { command: "b" }] } };
    mergeEntryIntoContainer(document, ["hooks", "stop"], ownEntry);
    expect(containerAt(document, ["hooks", "stop"]).map((entry) => (entry as { command: string }).command)).toEqual([
      "a",
      "b",
      ownEntry.command,
    ]);
  });
});

describe("removing only what is ours", () => {
  it("removes its own entry and keeps the foreign one", () => {
    const document: Record<string, unknown> = { hooks: { stop: [foreignEntry, ownEntry] } };
    expect(removeOwnEntriesFromContainer(document, ["hooks", "stop"])).toBe(1);
    expect(containerAt(document, ["hooks", "stop"])).toEqual([foreignEntry]);
  });

  it("deletes the container the removal emptied", () => {
    const document: Record<string, unknown> = { model: "sonnet", hooks: { stop: [ownEntry] } };
    removeOwnEntriesFromContainer(document, ["hooks", "stop"]);
    expect(document).toEqual({ model: "sonnet" });
  });

  it("removes nothing when the container holds no entry of ours", () => {
    const document: Record<string, unknown> = { hooks: { stop: [foreignEntry] } };
    expect(removeOwnEntriesFromContainer(document, ["hooks", "stop"])).toBe(0);
    expect(containerAt(document, ["hooks", "stop"])).toEqual([foreignEntry]);
  });

  it("prunes empty containers left behind", () => {
    const document: Record<string, unknown> = { hooks: {}, other: [] };
    pruneEmptyContainers(document);
    expect(documentIsEmpty(document)).toBe(true);
  });
});

describe("recognition by marker", () => {
  it("recognizes the marker anywhere inside a value", () => {
    expect(valueCarriesMarker(ownEntry)).toBe(true);
    expect(valueCarriesMarker({ args: [`/x/${OWNERSHIP_MARKER}/mcp-server.mjs`] })).toBe(true);
    expect(valueCarriesMarker(foreignEntry)).toBe(false);
    expect(valueCarriesMarker({ command: undefined })).toBe(false);
  });

  it("seeds a schema version only when the host config has none", () => {
    const document: Record<string, unknown> = {};
    seedSchemaVersion(document, "version", "1");
    seedSchemaVersion(document, "version", "2");
    expect(document.version).toBe("1");
  });
});

describe("writing JSON", () => {
  it("writes pretty-printed JSON with a trailing newline and creates parent directories", async () => {
    const path: string = home.path("deep", "nested", "config.json");
    await writeJsonFile(path, { serverMode: "self-hosted" });
    await expect(readFile(path, "utf8")).resolves.toBe('{\n  "serverMode": "self-hosted"\n}\n');
  });

  it("writes per-user state owner-only", async () => {
    if (process.platform === "win32") {
      return;
    }
    const path: string = home.path("config.json");
    await writeJsonFile(path, { bankId: "default" });
    expect((await stat(path)).mode & 0o077).toBe(0);
  });
});

describe("the sibling backup", () => {
  it("captures the pre-existing content and never overwrites it later", async () => {
    const path: string = home.path("host", "settings.json");
    await home.write("host/settings.json", '{"model":"sonnet"}\n');
    const first: string | null = await backupExistingFile(path);
    expect(first).toBe(backupPathFor(path));
    await expect(readFile(String(first), "utf8")).resolves.toBe('{"model":"sonnet"}\n');

    await writeFile(path, '{"model":"sonnet","hooks":{}}\n');
    const second: string | null = await backupExistingFile(path);
    expect(second).toBe(backupPathFor(path));
    await expect(readFile(String(second), "utf8")).resolves.toBe('{"model":"sonnet"}\n');
  });

  it("writes no backup for a file that did not exist", async () => {
    const path: string = home.path("host", "missing.json");
    await expect(backupExistingFile(path)).resolves.toBeNull();
    expect(await home.exists("host/missing.json.wenyuan-memory.bak")).toBe(false);
  });
});
