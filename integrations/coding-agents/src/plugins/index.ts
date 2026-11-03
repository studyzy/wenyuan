/**
 * Persistent-plugin root entry. opencode v1 loads the package root (or this
 * re-export of it) as a default-exported plugin, while opencode v2 loads
 * <directory>/index.js — one registered path therefore serves both host
 * generations. Every other plugin host names its own file through the package
 * "exports" map instead.
 */
export { wenyuanOpencodePlugin, default } from "./opencode.js";
