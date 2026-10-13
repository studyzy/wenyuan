import { resolveServerConfig } from "../../../../../../lib/server/config";
import { bankScopedPath, proxyUpstream, searchParamsObject } from "../../../../../../lib/server/proxy";

type Context = { params: Promise<{ bankId: string; webhookId: string }> };

function webhookPath(bankId: string, webhookId: string): string {
  return bankScopedPath("/v1/default/banks", bankId, `/webhooks/${encodeURIComponent(webhookId)}`);
}

/** Per-webhook sub-resource: PATCH updates, DELETE removes. */
export async function PATCH(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const { bankId, webhookId } = await context.params;
  const body = await request.text();
  return proxyUpstream({
    config,
    request,
    upstreamPath: webhookPath(bankId, webhookId),
    method: "PATCH",
    body,
  });
}

export async function DELETE(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const { bankId, webhookId } = await context.params;
  return proxyUpstream({
    config,
    request,
    upstreamPath: webhookPath(bankId, webhookId),
    query: searchParamsObject(request),
    method: "DELETE",
  });
}