import { resolveServerConfig } from "../../../../../../lib/server/config";
import { bankScopedPath, proxyUpstream } from "../../../../../../lib/server/proxy";

type Context = { params: Promise<{ bankId: string; alias: string }> };

function aliasPath(bankId: string, alias: string): string {
  return bankScopedPath("/v1/default/banks", bankId, `/aliases/${encodeURIComponent(alias)}`);
}

/** Per-alias sub-resource: PATCH promotes, DELETE removes. */
export async function PATCH(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const { bankId, alias } = await context.params;
  const body = await request.text();
  return proxyUpstream({
    config,
    request,
    upstreamPath: aliasPath(bankId, alias),
    method: "PATCH",
    body,
  });
}

export async function DELETE(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const { bankId, alias } = await context.params;
  return proxyUpstream({
    config,
    request,
    upstreamPath: aliasPath(bankId, alias),
    method: "DELETE",
  });
}