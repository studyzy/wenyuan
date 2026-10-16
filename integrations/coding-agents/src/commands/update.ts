import { beginGroup, describeError, fail, say } from "../cli/log";
import { stageRuntime } from "../runtime/stage";

/**
 * The update command.
 *
 * It re-stages the packaged runtime and returns: no host selection, no host
 * wiring, no host config touched. That is the whole contract, because the
 * runtime's own self-update path spawns it unattended and every wired host
 * config keeps pointing at the same stable directory.
 */
export async function update(): Promise<number> {
  beginGroup("runtime");
  try {
    const staged = await stageRuntime();
    say(`re-staged ${staged.files.length} runtime file(s) at ${staged.directory}`);
    return 0;
  } catch (cause) {
    fail(describeError(cause));
    return 1;
  }
}
