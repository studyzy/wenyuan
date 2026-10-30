/**
 * Session subjects.
 *
 * A session cookie records what it was signed in with. The gate reads this to
 * separate the three roles the console serves:
 *
 *   - `console` — the operator key (WENYUAN_CP_ADMIN_API_KEY): console plus the
 *     management plane;
 *   - `data` — the data key (WENYUAN_CP_DATAPLANE_API_KEY): console plus API,
 *     never the management plane;
 *   - `tenant:<id>` — a tenant key: one tenant's schema, never the management
 *     plane.
 *
 * This module exists so the Edge gate, the Node proxy and the login route agree
 * on the strings; they cannot share a module that touches either runtime's
 * crypto.
 */
export const OPERATOR_SUBJECT = "console";
export const DATA_SUBJECT = "data";

/** Subject a session entered with a tenant key carries. */
export function tenantSubject(tenantId: string): string {
  return `tenant:${tenantId}`;
}
