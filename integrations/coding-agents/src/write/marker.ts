/**
 * The one stable substring that every command path this package writes into a
 * host config carries, and the only thing install, uninstall and update use to
 * recognize their own entries.
 *
 * It is a fixed spelling rather than something derived from the path the
 * command happens to run from: the same installed hook has to be recognized
 * after the package is replaced by a newer version, and the stable runtime
 * directory carries the marker in its own name so the paths survive staging.
 */
export const OWNERSHIP_MARKER = "wenyuan-memory";

/**
 * True when a single string carries the marker, for example a hook command
 * path or one argument of an MCP server entry.
 */
export function carriesMarker(value: string): boolean {
  return value.includes(OWNERSHIP_MARKER);
}

/**
 * True when any string anywhere inside an arbitrary value carries the marker.
 * Entries are matched this way rather than by an added flag, so an entry
 * written by an older version of this package is still recognized.
 */
export function valueCarriesMarker(value: unknown): boolean {
  if (typeof value === "string") {
    return carriesMarker(value);
  }
  if (Array.isArray(value)) {
    return value.some((item) => valueCarriesMarker(item));
  }
  if (value !== null && typeof value === "object") {
    return Object.values(value).some((member) => valueCarriesMarker(member));
  }
  return false;
}
