import { resolveServerConfig } from "../../../../../../lib/server/config";
import { bankScopedPath, proxyUpstream, searchParamsObject } from "../../../../../../lib/server/proxy";
import { jsonResponse } from "../../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string; directiveId: string }> };

function directivePath(bankId: string, directiveId: string): string {
  return bankScopedPath(
    "/v1/default/banks",
    bankId,
    `/directives/${encodeURIComponent(directiveId)}`,
  );
}

/** Per-directive sub-resource: GET reads, PATCH updates. */
export async function GET(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const { bankId, directiveId } = await context.params;
  return proxyUpstream({
    config,
    request,
    upstreamPath: directivePath(bankId, directiveId),
    query: searchParamsObject(request),
    method: "GET",
  });
}

export async function PATCH(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const { bankId, directiveId } = await context.params;
  const body = await request.text();
  return proxyUpstream({
    config,
    request,
    upstreamPath: directivePath(bankId, directiveId),
    method: "PATCH",
    body,
  });
}

/**
 * Deleting a directive returns a small success payload rather than the upstream
 * delete body.
 */
export async function DELETE(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const { bankId, directiveId } = await context.params;
  const response = await proxyUpstream({
    config,
    request,
    upstreamPath: directivePath(bankId, directiveId),
    method: "DELETE",
  });
  if (!response.ok) return response;
  return jsonResponse({ success: true });
}