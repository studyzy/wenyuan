import { resolveServerConfig } from "../../../../lib/server/config";
import { bankScopedPath, proxyUpstream } from "../../../../lib/server/proxy";

type Context = { params: Promise<{ bankId: string }> };

/** Bank-scoped lifecycle: PUT/PATCH update, DELETE removes. */
export async function PATCH(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  return proxyUpstream({
    config,
    request,
    upstreamPath: bankScopedPath("/v1/default/banks", (await context.params).bankId),
    method: "PATCH",
    body,
  });
}

export async function PUT(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  return proxyUpstream({
    config,
    request,
    upstreamPath: bankScopedPath("/v1/default/banks", (await context.params).bankId),
    method: "PUT",
    body,
  });
}

export async function DELETE(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  return proxyUpstream({
    config,
    request,
    upstreamPath: bankScopedPath("/v1/default/banks", (await context.params).bankId),
    method: "DELETE",
  });
}