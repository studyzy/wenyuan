import { spawn } from "node:child_process";

/**
 * Dcode's plugin CLI runner.
 *
 * The plugin manager owns its cache and enablement state, so the adapter stays
 * a thin invocation: every command runs with an argv array, never a shell, and
 * a refused or missing CLI is reported rather than thrown, because the manual
 * remedy is the operator's next step.
 */

/** The marketplace name this package registers its local plugin under. */
export const DCODE_MARKETPLACE = "wenyuan-coding-agents";

/** The plugin id Dcode installs: the plugin name, then the marketplace's. */
export const DCODE_PLUGIN_ID = "wenyuan-coding-agents@wenyuan-coding-agents";

/** Runs one dcode plugin command; false when the CLI is unusable or refuses. */
export function runDcodePlugin(args: readonly string[], executable = "dcode"): Promise<boolean> {
  return new Promise((resolve) => {
    let child;
    try {
      child = spawn(executable, [...args], { stdio: "ignore", shell: false });
    } catch {
      resolve(false);
      return;
    }
    child.on("error", () => resolve(false));
    child.on("close", (code: number | null) => resolve(code === 0));
  });
}
