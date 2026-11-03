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

/**
 * WorkBuddy's MCP server-map location. It is a single fixed file, unlike
 * CodeBuddy's priority-ordered candidates: WorkBuddy reads one path and no
 * fallback, so a candidate list here would let an install write a file the host
 * never opens.
 */
export const WORKBUDDY_MCP_PATH = ".workbuddy/mcp.json";

/** The TOML host's single fixed config location. */
export const TOML_HOST_PATH = ".codex/config.toml";

/** The second TOML host's config, which carries its MCP server tables. */
export const GROK_CONFIG_PATH = ".grok/config.toml";

/** Devin keeps its hooks in its config.json and its servers beside them. */
export const DEVIN_CONFIG_PATH = ".config/devin/config.json";
export const DEVIN_MCP_PATH = ".config/devin/mcp_config.json";

/** Antigravity reads the Gemini CLI's config directory. */
export const ANTIGRAVITY_MCP_PATH = ".gemini/config/mcp_config.json";

/** Qwen's settings.json carries its hook registrations. */
export const QWEN_CONFIG_PATH = ".qwen/settings.json";

/** Factory Droid wires everything through plain JSON files under ~/.factory. */
export const DROID_MCP_PATH = ".factory/mcp.json";

/** Kimi Code keeps its MCP registration beside its config.toml. */
export const KIMI_MCP_PATH = ".kimi-code/mcp.json";

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
    hostId: "workbuddy",
    mechanism: "json-server-map",
    location: { kind: "fixed", path: WORKBUDDY_MCP_PATH },
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
  // Antigravity reads the Gemini CLI's config directory: hooks and MCP under
  // .gemini/config, the executable named agy.
  {
    hostId: "antigravity-cli",
    mechanism: "json-server-map",
    location: { kind: "fixed", path: ANTIGRAVITY_MCP_PATH },
    containerKey: MCP_SERVERS_KEY,
  },
  {
    hostId: "devin-cli",
    mechanism: "json-server-map",
    location: { kind: "fixed", path: DEVIN_MCP_PATH },
    containerKey: MCP_SERVERS_KEY,
  },
  // Grok Build's MCP server is a table in the same config.toml its CLI reads.
  {
    hostId: "grok-build",
    mechanism: "toml-table",
    location: { kind: "fixed", path: GROK_CONFIG_PATH },
    containerKey: MCP_TOML_CONTAINER_KEY,
  },
  // Qwen registers through its own command interface, spelled the Gemini CLI's
  // way: short flags, the environment pairs before the server name, and no
  // terminator, because everything after the name is the server's argv.
  {
    hostId: "qwen-code",
    mechanism: "command-line",
    location: { kind: "fixed", path: "" },
    argvStyle: "gemini",
  },
  // Factory Droid reads a server map under ~/.factory/mcp.json and reloads it
  // on change, so the registration is a plain file write.
  {
    hostId: "factory-droid",
    mechanism: "json-server-map",
    location: { kind: "fixed", path: DROID_MCP_PATH },
    containerKey: MCP_SERVERS_KEY,
  },
  // ZCode's server map is nested in its CLI config: mcp.servers, not the
  // top-level mcpServers the other JSON hosts read.
  {
    hostId: "zcode",
    mechanism: "json-server-map",
    location: { kind: "fixed", path: ".zcode/cli/config.json" },
    containerPath: ["mcp", "servers"],
  },
  // Kimi keeps its MCP registration in its own mcp.json, not config.toml; the
  // packaged stdio server reads the deployment contract from this package's
  // own config file, so the entry needs no token member.
  {
    hostId: "kimi",
    mechanism: "json-server-map",
    location: { kind: "fixed", path: KIMI_MCP_PATH },
    containerKey: MCP_SERVERS_KEY,
  },
  // Deliberately absent: traecode declares no user-level registration, because
  // Trae launches user-level servers with the Electron process's working
  // directory, where an opt-in-only server self-disables and an always-on one
  // resolves the home bank instead of the repository's. The only correct
  // registration is the per-repository .trae/mcp.json file the session-start
  // wiring maintains; the adapter migrates a stale user-level entry of ours
  // away instead of writing one. dcode is absent for the opposite reason: its
  // plugin carries the tools, so no server entry is registered at all.
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

/** The nested server-map path a JSON host reads, or undefined for the others. */
export function containerPathFor(hostId: string): readonly string[] | undefined {
  return mechanismFor(hostId)?.containerPath;
}

/** The command-interface argv spelling a command-line host accepts. */
export function argvStyleFor(hostId: string): "default" | "gemini" | undefined {
  return mechanismFor(hostId)?.argvStyle;
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