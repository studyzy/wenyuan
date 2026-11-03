/**
 * Kilo Code CLI entrypoint (`kilo`).
 *
 * Kilo is a FORK OF OPENCODE: same config schema (it still reads legacy `opencode.json`), the same
 * `plugin` array. So Kilo needs no adapter of its own — it loads this default export as a
 * persistent plugin and drives the exact opencode runtime (recall+inject, cold seed, native
 * wenyuan_* tools, write-back).
 *
 * Kilo has NO hooks system, which is why it is a plugin harness rather than a hook-binary harness
 * like claude-code/cursor/codex.
 *
 * This entry exists — rather than pointing Kilo at the opencode bundle — purely so the harness
 * reports as "kilo": that selects the `harnesses.kilo` config section, feeds `{harness}` bank
 * templating, and keeps Kilo sessions attributable in diagnostics separately from opencode's.
 */
import { createPluginEntry } from "../harness/plugin-entry";

const wenyuanKiloPlugin = createPluginEntry("kilo");

export default wenyuanKiloPlugin;
export { wenyuanKiloPlugin };
