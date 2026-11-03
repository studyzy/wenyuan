import { zstdCompressSync } from "node:zlib";
import { describe, expect, it, vi } from "vitest";

// Simulate a Node build without Zstandard support: the module under test reads its decoder off
// node:zlib, so the mock strips exactly that export and leaves the rest of the module intact.
vi.mock("node:zlib", async (importOriginal) => {
  const actual = await importOriginal<typeof import("node:zlib")>();
  return { ...actual, zstdDecompressSync: undefined };
});

const { scanZstdFrames, zstdDecompressFrames } = await import("../../../src/core/zstd-frames");

describe("zstdDecompressFrames on a runtime without zstd", () => {
  it("fails with a message naming the missing API, and the scan still works", () => {
    // The scan is pure byte-walking — it must not depend on the decoder. The decode must refuse
    // with a clear statement of what is missing, never an opaque "not a function" TypeError.
    const buffer = zstdCompressSync(Buffer.from("line\n", "utf8"));
    expect(scanZstdFrames(buffer)).toHaveLength(1);
    expect(() => zstdDecompressFrames(buffer)).toThrow(/zstdDecompressSync/);
  });
});
