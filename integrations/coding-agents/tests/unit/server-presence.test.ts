import { describe, expect, it } from "vitest";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { MCP_SERVER_FILE_NAME } from "../../src/runtime/layout";
import { packagedServerReason, stagedServerFileExists } from "../../src/runtime/server-presence";

/**
 * The guard between a staged runtime and the MCP entries that name it.
 *
 * Every host's server entry is the absolute path of the packaged server, so a
 * runtime that did not stage that file would wire hosts whose memory tools
 * never start. The guard answers one question — is the file there — and names
 * what is missing when it is not.
 */

async function withRuntime<T>(files: readonly string[], body: (directory: string) => Promise<T>): Promise<T> {
  const directory: string = await mkdtemp(join(tmpdir(), "wenyuan-runtime-"));
  try {
    for (const file of files) {
      const target: string = join(directory, file);
      // Staging lays the entry points out in subdirectories, so the fixture
      // creates a file's parent the way the real copy does.
      await mkdir(dirname(target), { recursive: true });
      await writeFile(target, "// staged\n");
    }
    return await body(directory);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
}

describe("the packaged server's presence in a staged runtime", () => {
  it("reports the file present when staging produced it", async () => {
    await withRuntime([MCP_SERVER_FILE_NAME], async (directory) => {
      expect(await stagedServerFileExists(directory)).toBe(true);
      expect(await packagedServerReason(directory)).toBeNull();
    });
  });

  it("names the missing file instead of wiring a host that would expose nothing", async () => {
    await withRuntime(["hooks/session-start.mjs"], async (directory) => {
      expect(await stagedServerFileExists(directory)).toBe(false);
      const reason: string | null = await packagedServerReason(directory);
      expect(reason).not.toBeNull();
      expect(reason).toContain(MCP_SERVER_FILE_NAME);
      expect(reason).toContain(directory);
    });
  });

  it("counts a directory sitting at the file's name as missing", async () => {
    const directory: string = await mkdtemp(join(tmpdir(), "wenyuan-runtime-"));
    try {
      await mkdir(join(directory, MCP_SERVER_FILE_NAME));
      expect(await stagedServerFileExists(directory)).toBe(false);
    } finally {
      await rm(directory, { recursive: true, force: true });
    }
  });
});