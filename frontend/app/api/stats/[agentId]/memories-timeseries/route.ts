import { resolveServerConfig } from "../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../lib/server/http";

type Context = { params: Promise<{ agentId: string }> };

const SERIES_DEFAULTS: Record<string, string> = { period: "7d", time_field: "created_at" };

/**
 * Memory time series for the dashboard charts.
 *
 * Returns 200 with the time-series payload. `period` and `time_field` carry
 * their documented defaults when the caller omits them.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
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
      query: {
        period: searchParams.get("period") || SERIES_DEFAULTS.period,
        time_field: searchParams.get("time_field") || SERIES_DEFAULTS.time_field,
      },
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
