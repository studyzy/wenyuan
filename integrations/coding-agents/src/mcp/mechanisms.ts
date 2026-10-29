import { OWNERSHIP_MARKER } from "../write/marker";
import type {
  EntryMemberNames,
  LocationResolution,
  McpMechanismDeclaration,
  MechanismKind,
} from "./mechanisms-types";

/**
 * The single declaration of per-host MCP registration.
 *
 * Each approved host declares which registration mechanism it uses and how its
 * MCP config location resolves; every mechanism resolves through this module
 * rather than spelling a member name or a location a second time. Nothing here
 * reads or writes a file or invokes a host command: this is the one place the
 * member names, the mechanism choice and the location resolutions live.
 */

/** Wire contract: the server reads this to attribute the caller. */
export const MCP_HARNESS_ENV = "WENYUAN_MCP_HARNESS";

/** This package's server name, which doubles as the ownership marker. */
export const MCP_SERVER_NAME = OWNERSHIP_MARKER;

/** The launcher a host execs; the packaged server path is its first argument. */
export const MCP_LAUNCHER_COMMAND = "node";

/** The JSON server-map container key the JSON host reads verbatim. */
export const MCP_SERVERS_KEY = "mcpServers";

/** The TOML table-container key the TOML host reads verbatim. */
export const MCP_TOML_CONTAINER_KEY = "mcp_servers";

/** The three entry member names, declared once. */
export const MCP_ENTRY_MEMBERS: EntryMemberNames = {
  command: "command",
  args: "args",
  environment: "env",
};

/** The JSON host's candidate locations, most preferred first. */
export const JSON_HOST_CANDIDATES: readonly string[] = [
  ".codebuddy/.mcp.json",
  ".codebuddy/mcp.json",
  ".codebuddy.json",
];

/** A second JSON host's single fixed server-map location, under its home directory. */
export const DSH_HOST_PATH = ".dsh/mcp.json";

/** The TOML host's single fixed config location. */
export const TOML_HOST_PATH = ".codex/config.toml";

/** The two flat-entry hosts' single fixed JSON server-map locations. */
export const CURSOR_MCP_PATH = ".cursor/mcp.json";
export const COPILOT_MCP_PATH = ".copilot/mcp-config.json";

/**
 * The per-host mechanism declaration. The command-line host declares no
 * container key because it writes no file of ours: its registration lives in
 * the host's own store.
 */
export const MECHANISMS: readonly McpMechanismDeclaration[] = [
  {
    hostId: "claude-code",
    mechanism: "command-line",
    location: { kind: "fixed", path: "" },
  },
  {
    hostId: "codex",
    mechanism: "toml-table",
    location: { kind: "fixed", path: TOML_HOST_PATH },
    containerKey: MCP_TOML_CONTAINER_KEY,
  },
  {
    hostId: "codebuddy",
    mechanism: "json-server-map",
    location: { kind: "candidates", paths: JSON_HOST_CANDIDATES },
    containerKey: MCP_SERVERS_KEY,
  },
  {
    hostId: "cursor-cli",
    mechanism: "json-server-map",
    location: { kind: "fixed", path: CURSOR_MCP_PATH },
    containerKey: MCP_SERVERS_KEY,
  },
  {
    hostId: "copilot-cli",
    mechanism: "json-server-map",
    location: { kind: "fixed", path: COPILOT_MCP_PATH },
    containerKey: MCP_SERVERS_KEY,
  },
  {
    hostId: "dsh",
    mechanism: "json-server-map",
    location: { kind: "fixed", path: DSH_HOST_PATH },
    containerKey: MCP_SERVERS_KEY,
  },
];

/** The declared mechanism for a host, or undefined for an unapproved host. */
export function mechanismFor(hostId: string): McpMechanismDeclaration | undefined {
  return MECHANISMS.find((declaration) => declaration.hostId === hostId);
}

/** The mechanism kind for a host, or undefined for an unapproved host. */
export function mechanismKindFor(hostId: string): MechanismKind | undefined {
  return mechanismFor(hostId)?.mechanism;
}

/** A host's declared location resolution, or undefined for an unapproved host. */
export function locationResolutionFor(hostId: string): LocationResolution | undefined {
  return mechanismFor(hostId)?.location;
}

/** The container key a file host reads, or undefined for the command-line host. */
export function containerKeyFor(hostId: string): string | undefined {
  return mechanismFor(hostId)?.containerKey;
}

/** The approved host identifiers, in declared order. */
export function declaredMechanismHosts(): string[] {
  return MECHANISMS.map((declaration) => declaration.hostId);
}

/**
 * A host config is either plain JSON or JSON that permits comments. The file
 * hosts here keep plain JSON; the type is re-exported so a mechanism and the
 * write primitives agree on the spelling.
 */
export type { ConfigFormat } from "../write/json-merge";