// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { CopyButton } from "../../components/ui/copy-button";

vi.mock("next-intl", () => ({ useTranslations: () => (key: string) => key }));

afterEach(() => {
  cleanup();
  Reflect.deleteProperty(navigator, "clipboard");
  Reflect.deleteProperty(document, "execCommand");
  vi.restoreAllMocks();
});

describe("CopyButton", () => {
  it("copies the value and confirms it", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });

    render(<CopyButton value="wyk_abc" />);
    fireEvent.click(screen.getByRole("button", { name: "copy" }));

    await waitFor(() => expect(writeText).toHaveBeenCalledWith("wyk_abc"));
    await waitFor(() => expect(screen.getByRole("button", { name: "copied" })).toBeTruthy());
  });

  it("still copies when the Clipboard API is unavailable", async () => {
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
    const execCommand = vi.fn().mockReturnValue(true);
    Object.defineProperty(document, "execCommand", { configurable: true, value: execCommand });

    render(<CopyButton value="wyk_fallback" />);
    fireEvent.click(screen.getByRole("button", { name: "copy" }));

    await waitFor(() => expect(execCommand).toHaveBeenCalledWith("copy"));
    await waitFor(() => expect(screen.getByRole("button", { name: "copied" })).toBeTruthy());
  });
});