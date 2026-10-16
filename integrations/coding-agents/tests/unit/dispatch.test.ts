import { describe, expect, it } from "vitest";
import { COMMANDS } from "../../src/cli/argv";
import { run } from "../../src/cli/main";
import { usageText } from "../../src/cli/usage";
import { captureOutput, withFakeHome, type InstallerRun } from "../helpers/fake-home";

/**
 * The dispatcher: four command words, usage and exit codes for a missing or
 * unrecognized one, and nothing written either way.
 *
 * The read-only command is dispatched here rather than through the built
 * bundle, so this suite stays offline even before the package is built.
 */

async function runInHome(argv: string[]): Promise<InstallerRun> {
  return withFakeHome({ environment: { PATH: "" } }, async () => captureOutput(() => run(argv)));
}

describe("the dispatcher", () => {
  it("prints the usage text and exits zero when no command word was given", async () => {
    const result: InstallerRun = await runInHome([]);
    expect(result.status).toBe(0);
    expect(result.stdout).toContain(usageText().split("\n")[0] ?? "");
    for (const command of COMMANDS) {
      expect(result.stdout).toContain(command);
    }
    expect(result.stderr).toBe("");
  });

  it("prints the usage text and exits non-zero for an unrecognized command", async () => {
    const result: InstallerRun = await runInHome(["reinstall"]);
    expect(result.status).not.toBe(0);
    expect(result.stdout).toContain(usageText().split("\n")[0] ?? "");
    expect(result.stderr).toContain('unknown command "reinstall"');
  });

  it.each([...COMMANDS])("recognizes %s rather than reporting it unknown", async (command: string) => {
    const result: InstallerRun = await runInHome([command]);
    expect(result.combined()).not.toContain("unknown command");
    expect(result.stdout).not.toContain("Usage:");
  });

  it("explains the all-hosts keyword when a wiring command has no host selection", async () => {
    for (const command of ["install", "uninstall"]) {
      const result: InstallerRun = await runInHome([command]);
      expect(result.status).not.toBe(0);
      expect(result.combined()).toContain("all");
      expect(result.combined()).toContain("claude-code");
    }
  });

  it("writes nothing for a missing or an unrecognized command", async () => {
    for (const argv of [[], ["reinstall"], ["--server", "local"]]) {
      await withFakeHome({ environment: { PATH: "" } }, async (home) => {
        await captureOutput(() => run(argv));
        expect(await home.list()).toEqual([]);
      });
    }
  });

  it("handles the read-only command without staging a runtime", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home) => {
      const result: InstallerRun = await captureOutput(() => run(["stats"]));
      expect(result.status).toBe(0);
      expect(result.stdout).toContain("no recorded usage yet");
      expect(await home.exists(".wenyuan")).toBe(false);
    });
  });
});
