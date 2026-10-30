import { resolveServerConfig } from "../../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string; logId: string }> };

function auditLogPath(bankId: string, logId: string): string {
  return bankScopedPath(
    "/v1/default/banks",
    bankId,
    `/audit-logs/${encodeURIComponent(logId)}`,
  );
}

/**
 * Single audit-log entry.
 *
 * The list endpoint previews entries without request/response bodies; this
 * fetch returns the full entry for the detail dialog.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { bankId, logId } = await context.params;
  if (!bankId || !logId) {
    return errorResponse("api.errors.validation.logIdRequired", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "audit_log_get_failed",
    errorKey: "api.errors.auditLogs.get",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: auditLogPath(bankId, logId),
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
