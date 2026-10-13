import { resolveServerConfig } from "../../../../../../lib/server/config";
import { bankScopedPath, proxyUpstream, searchParamsObject } from "../../../../../../lib/server/proxy";

type Context = { params: Promise<{ bankId: string; operationId: string }> };

/** Per-operation lookup, retry, and delete. */
export async function GET(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const { bankId, operationId } = await context.params;
  return proxyUpstream({
    config,
    request,
    upstreamPath: bankScopedPath(
      "/v1/default/banks",
      bankId,
      `/operations/${encodeURIComponent(operationId)}`,
    ),
    query: searchParamsObject(request),
    method: "GET",
  });
}

export async function POST(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const { bankId, operationId } = await context.params;
  const body = await request.text();
  return proxyUpstream({
    config,
    request,
    upstreamPath: bankScopedPath(
      "/v1/default/banks",
      bankId,
      `/operations/${encodeURIComponent(operationId)}`,
    ),
    query: searchParamsObject(request),
    method: "POST",
    body: body.length > 0 ? body : undefined,
  });
}

export async function DELETE(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const { bankId, operationId } = await context.params;
  return proxyUpstream({
    config,
    request,
    upstreamPath: bankScopedPath(
      "/v1/default/banks",
      bankId,
      `/operations/${encodeURIComponent(operationId)}`,
    ),
    query: searchParamsObject(request),
    method: "DELETE",
  });
}