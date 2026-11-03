/**
 * opencode entrypoint — opencode loads the default export as a persistent v1 plugin.
 *
 * opencode runs the SAME v2 surface as the hook harnesses, just delivered through opencode's plugin
 * hooks instead of a fresh process per event:
 *   READ   — each user turn, recall on the prompt and PUSH the memory block (with the attribution
 *            + user-feedback framing) into the system prompt.
 *   SEED   — on load, cold-check the bank and (if cold) start a background git-log seed + codebase
 *            survey. (The knowledge-page preamble is built per session, on the first prompt.)
 *   TOOLS  — register the wenyuan_* knowledge/recall suite natively (no MCP server needed).
 *   WRITE  — on by default: upsert the rich transcript on the server-side fold, and again on
 *            session.idle — only the idle pass can see the agent's reply.
 *
 * The recall/inject/seed/write-back logic is a harness-agnostic RuntimeCore; the opencode adapter
 * binds it to opencode's plugin API. All configuration comes from ~/.wenyuan/config.json (no
 * environment variables) — see core/config.ts for the shape and defaults.
 *
 * The body lives in harness/plugin-entry.ts, shared with the Kilo entry (kilo.ts) — Kilo CLI is an
 * opencode fork running the identical plugin contract.
 */
import { createPluginEntry } from "../harness/plugin-entry";

const wenyuanOpencodePlugin = createPluginEntry("opencode");

export default wenyuanOpencodePlugin;
export { wenyuanOpencodePlugin };
