/**
 * Reflect (the synthesized root-cause answer) and raw recall — the read paths.
 */

import { ReflectError } from "./errors";
import type { ClientInternals } from "./transport";

/**
 * Reflect: a synthesized, root-cause answer over the bank. Bounded so a slow server never hangs a
 * caller — but timeoutMs is REQUIRED, deliberately: the right deadline differs by an order of
 * magnitude between the automatic hook (20s, to fit the host's window) and the agent-invoked tool
 * (minutes, on a populated bank). A default here would silently override the tool's configured
 * window and abort every high-budget synthesis mid-flight.
 */
export async function reflect(
  c: ClientInternals,
  query: string,
  opts: { budget?: string; timeoutMs: number }
): Promise<string> {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), opts.timeoutMs);
  try {
    const resp = await c.fetchWithAuth(c.bankUrl("/reflect"), {
      method: "POST",
      body: JSON.stringify({ query, budget: opts.budget ?? "high" }),
      signal: ctrl.signal,
    });
    // Keep the server's body: a bare "reflect 500" in the diag trail is undebuggable afterwards.
    if (!resp.ok)
      throw new ReflectError(
        `reflect ${resp.status} ${(await resp.text()).slice(0, 1000)}${c.authHint(resp.status)}`,
        resp.status,
        false
      );
    const data = (await resp.json()) as { text?: string };
    return (data.text || "").trim();
  } catch (e) {
    // Our own deadline surfaces as a generic "This operation was aborted"; name it.
    if (ctrl.signal.aborted)
      throw new ReflectError(`reflect timed out after ${opts.timeoutMs}ms`, undefined, true, {
        cause: e,
      });
    throw e;
  } finally {
    clearTimeout(timer);
  }
}

/**
 * Raw recall with no LLM in the loop, so it still answers when reflect's synthesis times out or
 * 5xxs. The body is recallOptions (consolidated observations by default) — a bank that grows no
 * observations widens it rather than getting nothing back. Returns the texts in rank order. The
 * name predates recallOptions and is kept deliberately: this is the client's published surface.
 */
export async function recallObservations(
  c: ClientInternals,
  query: string,
  opts: { timeoutMs: number }
): Promise<string[]> {
  const r = await c.req(
    "POST",
    c.bankUrl("/memories/recall"),
    // query is applied AFTER the spread: everything else is the caller's to override, but a
    // config that could replace the goal with a fixed string would silently recall for the
    // wrong question on every turn.
    { ...c.recallOptions, query },
    [],
    opts.timeoutMs
  );
  if (r.status === 404) return [];
  const j = (await r.json()) as { results?: { text?: string }[] };
  return (j.results ?? []).map((x) => (x.text ?? "").trim()).filter(Boolean);
}
