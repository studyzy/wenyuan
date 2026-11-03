/**
 * opencode2 entrypoint — the plugin opencode v2 loads.
 *
 * It is a SEPARATE entry from the v1 one because v2's plugin contract shares nothing with v1's:
 * v1 loads a function returning named hooks, v2 loads a `{id, setup(ctx)}` object. Handing either
 * host the other's export loads a plugin that registers nothing and reports no error.
 *
 * The two hosts nonetheless share one registered path. opencode v1 and v2 read the SAME
 * `~/.config/opencode/opencode.json`, and v1 REJECTS the whole file when it sees v2's `plugins`
 * key ("Configuration is invalid … Unrecognized key: plugins"), so a second config entry is not an
 * option. What makes one entry serve both is that they resolve a plugin DIRECTORY differently:
 * v1 follows `package.json` `main` (the v1 plugin), v2 looks for `<dir>/index.js` and ignores
 * `main`. The package therefore ships a root `index.js` re-exporting the v1 plugin, and the
 * package root already registered in the user's `plugin` array — which v2 migrates to `plugins`
 * for itself — drives the right plugin under each host.
 *
 * The harness reports as "opencode2" rather than "opencode": that selects the
 * `harnesses.opencode2` config section and keeps v2 sessions attributable in diagnostics
 * separately from v1's. The bank is unaffected — the default id template
 * (`coding-agent::{gitProject}`) is harness-neutral, so both hosts compound into the same repo
 * memory.
 *
 * The body lives in harness/opencode2.ts; everything behind it is the shared RuntimeCore.
 */
import { createOpencode2PluginEntry } from "../harness/opencode2";

const wenyuanOpencode2Plugin = createOpencode2PluginEntry("opencode2");

export default wenyuanOpencode2Plugin;
export { wenyuanOpencode2Plugin };
