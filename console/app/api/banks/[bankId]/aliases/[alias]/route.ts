import { resolveServerConfig } from "../../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string; alias: string }> };

function aliasPath(bankId: string, alias: string): string {
  return bankScopedPath("/v1/default/banks", bankId, `/aliases/${encodeURIComponent(alias)}`);
}

/**
 * Per-alias sub-resource: PATCH promotes an alias to primary, DELETE removes it.
 * Both return 200 with the upstream payload and leave the bank and its memories
 * untouched.
 */
export async function PATCH(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { bankId, alias } = await context.params;
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);
  if (!alias) return errorResponse("api.errors.validation.missingRequestData", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  const failure = { failureLabel: "alias_update_failed", errorKey: "api.errors.bankAliases.update", locale };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: aliasPath(bankId, alias),
      method: "PATCH",
      body,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}

export async function DELETE(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { bankId, alias } = await context.params;
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);
  if (!alias) return errorResponse("api.errors.validation.missingRequestData", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = { failureLabel: "alias_delete_failed", errorKey: "api.errors.bankAliases.delete", locale };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: aliasPath(bankId, alias),
      method: "DELETE",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
