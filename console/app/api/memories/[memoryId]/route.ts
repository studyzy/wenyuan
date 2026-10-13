import { resolveServerConfig } from "../../../../lib/server/config";
import { proxyUpstream, searchParamsObject } from "../../../../lib/server/proxy";
import { errorResponse, resolveBankScoping } from "../../../../lib/server/http";
import { requestLocale } from "../../../../lib/server/proxy";

type Context = { params: Promise<{ memoryId: string }> };

/** Per-memory read/update and its history child. */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const scoping = resolveBankScoping(new URL(request.url).searchParams);
  if (!scoping) {
    return errorResponse("errors.requiredBankId", 400, locale);
  }
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  return proxyUpstream({
    config,
    request,
    upstreamPath: `/v1/default/memories/${encodeURIComponent((await context.params).memoryId)}`,
    query: searchParamsObject(request),
    method: "GET",
  });
}

export async function PATCH(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  return proxyUpstream({
    config,
    request,
    upstreamPath: `/v1/default/memories/${encodeURIComponent((await context.params).memoryId)}`,
    method: "PATCH",
    body,
  });
}

export async function DELETE(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  return proxyUpstream({
    config,
    request,
    upstreamPath: `/v1/default/memories/${encodeURIComponent((await context.params).memoryId)}`,
    method: "DELETE",
  });
}