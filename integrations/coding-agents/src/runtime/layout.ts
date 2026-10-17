import { homedir } from "node:os";
import { join } from "node:path";
import { OWNERSHIP_MARKER } from "../write/marker";

/**
 * The stable per-user runtime directory and the entry points inside it.
 *
 * Every command path written into a host config points here and never into
 * the location the command happened to run from, so a package that is later
 * replaced or removed cannot break an install that is already wired. The
 * directory's own name carries the ownership marker, which is what keeps
 * recognition, replacement and removal working after staging.
 */

/** Kept in step with the per-user directory the stored configuration uses. */
export const PER_USER_DIRECTORY_NAME = ".wenyuan";
export const RUNTIME_DIRECTORY_NAME = OWNERSHIP_MARKER;
export const HOOKS_SUBDIRECTORY = "hooks";
export const MCP_SERVER_FILE_NAME = "mcp-server.mjs";

export function homeDirectory(): string {
  const fromEnvironment: string | undefined =
    process.env.HOME ?? process.env.USERPROFILE;
  if (fromEnvironment && fromEnvironment.length > 0) {
    return fromEnvironment;
  }
  return homedir();
}

export function runtimeDirectory(): string {
  return join(homeDirectory(), PER_USER_DIRECTORY_NAME, RUNTIME_DIRECTORY_NAME);
}

/** The absolute path a host launches as the packaged MCP server. */
export function mcpServerPath(): string {
  return join(runtimeDirectory(), MCP_SERVER_FILE_NAME);
}
