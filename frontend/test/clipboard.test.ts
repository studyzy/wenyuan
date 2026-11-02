// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { copyText } from "../lib/client/clipboard";

function setClipboard(value: unknown): void {
  Object.defineProperty(navigator, "clipboard", { configurable: true, value });
}

function setExecCommand(value: unknown): void {
  Object.defineProperty(document, "execCommand", { configurable: true, value });
}

afterEach(() => {
  Reflect.deleteProperty(navigator, "clipboard");
  Reflect.deleteProperty(document, "execCommand");
  vi.restoreAllMocks();
});

describe("copyText", () => {
  it("uses the Clipboard API when it is available", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    setClipboard({ writeText });

    await expect(copyText("wyk_abc")).resolves.toBe(true);
    expect(writeText).toHaveBeenCalledWith("wyk_abc");
  });

  it("falls back to a selection when the Clipboard API is missing", async () => {
    setClipboard(undefined);
    const execCommand = vi.fn().mockReturnValue(true);
    setExecCommand(execCommand);

    await expect(copyText("wyk_abc")).resolves.toBe(true);
    expect(execCommand).toHaveBeenCalledWith("copy");
    expect(document.querySelector("textarea")).toBeNull();
  });

  it("falls back when the Clipboard API rejects", async () => {
    setClipboard({ writeText: vi.fn().mockRejectedValue(new Error("denied")) });
    const execCommand = vi.fn().mockReturnValue(true);
    setExecCommand(execCommand);

    await expect(copyText("wyk_abc")).resolves.toBe(true);
    expect(execCommand).toHaveBeenCalledWith("copy");
  });

  it("reports failure when neither path is available", async () => {
    setClipboard(undefined);
    setExecCommand(undefined);

    await expect(copyText("wyk_abc")).resolves.toBe(false);
  });
});