import { resolveServerConfig } from "../../../../lib/server/config";
import {
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../lib/server/proxy";
import { errorResponse } from "../../../../lib/server/http";

type Context = { params: Promise<{ entityId: string }> };

function decodeEntityId(encoded: string): string {
  try {
    return decodeURIComponent(encoded);
  } catch {
    return encoded;
  }
}

/**
 * Per-entity read.
 *
 * The bank id is required. The entity id segment is decoded before forwarding
 * so ids containing reserved characters round-trip correctly upstream. Returns
 * 200 with the entity payload.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const bankId = searchParams.get("bank_id");
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const entityId = decodeEntityId((await context.params).entityId);
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "entity_fetch_failed",
    errorKey: "api.errors.entities.fetch",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: `/v1/default/banks/${encodeURIComponent(bankId)}/entities/${encodeURIComponent(entityId)}`,
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
