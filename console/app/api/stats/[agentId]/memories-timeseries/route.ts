import { resolveServerConfig } from "../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../lib/server/proxy";
import { errorResponse, passthroughQueryParams } from "../../../../../lib/server/http";

type Context = { params: Promise<{ agentId: string }> };

const SERIES_PARAMS = ["period", "start_date", "end_date"] as const;

/**
 * Memory time series for the dashboard charts.
 *
 * Returns 200 with the time-series payload.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = (await context.params).agentId;
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "memories_timeseries_failed",
    errorKey: "api.errors.stats.memoriesTimeseries",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/stats/memories-timeseries"),
      query: passthroughQueryParams(request, SERIES_PARAMS),
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
