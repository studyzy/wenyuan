/**
 * The shapes the MCP registration declaration is expressed in.
 *
 * A mechanism is named in this package's own vocabulary rather than by a host
 * identifier, so a mechanism and a host can never be confused: two hosts that
 * happen to register the same way still declare their own entry, and a host
 * that changes its mechanism moves its declaration rather than its module.
 */

/** How a host accepts an MCP server registration. */
export type MechanismKind = "command-line" | "toml-table" | "json-server-map";

/**
 * The three member names every entry carries, spelled once. The host's own
 * loader reads them verbatim, so a mechanism writes them through this record
 * rather than repeating the strings.
 */
export interface EntryMemberNames {
  /** The launcher command member, holding the node launcher. */
  command: string;
  /** The argv member, whose first element names the packaged server. */
  args: string;
  /** The environment member, carrying the harness-attribution variable. */
  environment: string;
}

/**
 * How a host's MCP config location resolves: one fixed path for a host that
 * accepts a single location, or a priority-ordered candidate list for a host
 * that accepts several, where the first existing candidate is written and
 * every candidate is swept.
 */
export type LocationResolution =
  | { readonly kind: "fixed"; readonly path: string }
  | { readonly kind: "candidates"; readonly paths: readonly string[] };

/** One approved host's declared registration mechanism and location form. */
export interface McpMechanismDeclaration {
  /** The host this declaration is for, spelled as the operator names it. */
  readonly hostId: string;
  /** Which mechanism registers this host's server entry. */
  readonly mechanism: MechanismKind;
  /** How this host's MCP config location resolves. */
  readonly location: LocationResolution;
  /**
   * The server-map or table-container key the host reads to discover servers,
   * for the file hosts; absent for a host whose registration is not a file of
   * ours.
   */
  readonly containerKey?: string;
}