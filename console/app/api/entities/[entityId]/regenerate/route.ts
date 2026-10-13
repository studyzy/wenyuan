import { resolveServerConfig } from "../../../../../lib/server/config";
import {
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../lib/server/http";

type Context = { params: Promise<{ entityId: string }> };

function decodeEntityId(encoded: string): string {
  try {
    return decodeURIComponent(encoded);
  } catch {
    return encoded;
  }
}

/**
 * Per-entity observation regeneration.
 *
 * The bank id is required. The entity id segment is decoded before forwarding.
 * Returns 200 with the regeneration payload.
 */
export async function POST(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const bankId = searchParams.get("bank_id");
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const entityId = decodeEntityId((await context.params).entityId);
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  const failure = {
    failureLabel: "entity_regenerate_failed",
    errorKey: "api.errors.entities.regenerateObservations",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: `/v1/default/banks/${encodeURIComponent(bankId)}/entities/${encodeURIComponent(entityId)}/regenerate`,
      method: "POST",
      body: body.length > 0 ? body : undefined,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
