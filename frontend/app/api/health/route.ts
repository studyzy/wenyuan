import { resolveServerConfig } from "../../../lib/server/config";
import { jsonResponse } from "../../../lib/server/http";
import { buildUpstreamUrl, upstreamHeaders } from "../../../lib/server/upstream";

const HEALTH_TIMEOUT_MS = 1500;
const SERVICE_NAME = "hindsight-control-plane";

/**
 * Health probe.
 *
 * Returns 200 with a status field, a service field naming the control plane,
 * and an optional dataplane object reporting connected/disconnected, the
 * upstream URL, and an error string. The upstream probe is bounded by a short
 * timeout and requests the smallest page.
 */
export async function GET(_request: Request): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const upstreamUrl = `${config.dataplaneUrl}/v1/default/banks?limit=1`;

  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), HEALTH_TIMEOUT_MS);

  try {
    const response = await fetch(buildUpstreamUrl(config, { path: "/v1/default/banks", query: { limit: "1" } }), {
      method: "GET",
      headers: upstreamHeaders(config),
      signal: controller.signal,
    });
    return jsonResponse({
      status: response.ok ? "ok" : "degraded",
      service: SERVICE_NAME,
      dataplane: {
        status: response.ok ? "connected" : "disconnected",
        url: upstreamUrl,
        ...(response.ok ? {} : { error: `upstream returned ${response.status}` }),
      },
    });
  } catch (error) {
    return jsonResponse({
      status: "degraded",
      service: SERVICE_NAME,
      dataplane: {
        status: "disconnected",
        url: upstreamUrl,
        error: error instanceof Error ? error.message : "upstream unreachable",
      },
    });
  } finally {
    clearTimeout(timer);
  }
}