import { resolveServerConfig } from "../../../lib/server/config";
import { jsonResponse, errorResponse } from "../../../lib/server/http";
import { callUpstream } from "../../../lib/server/upstream";
import { requestLocale } from "../../../lib/server/proxy";

/**
 * Version probe.
 *
 * Fetches upstream version data, injects the access-key-auth boolean feature
 * flag derived from the local environment, and returns it with 200. An upstream
 * failure yields a localized 500.
 */
export async function GET(request: Request): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const locale = requestLocale(request);
  const gateOn = config.accessKey.length > 0;

  try {
    const result = await callUpstream(config, { path: "/v1/version" });
    if (!result.ok) {
      return errorResponse("api.errors.version.unavailable", 500, locale);
    }
    const text = new TextDecoder().decode(result.body);
    const payload = text.length > 0 ? (JSON.parse(text) as Record<string, unknown>) : {};
    const features = (payload.features as Record<string, boolean> | undefined) ?? {};
    return jsonResponse(
      { ...payload, features: { ...features, access_key_auth: gateOn } },
      200,
    );
  } catch {
    return errorResponse("api.errors.version.unavailable", 500, locale);
  }
}