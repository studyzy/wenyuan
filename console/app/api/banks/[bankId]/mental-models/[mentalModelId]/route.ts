import { resolveServerConfig } from "../../../../../../lib/server/config";
import { bankScopedPath, proxyUpstream, searchParamsObject } from "../../../../../../lib/server/proxy";
import { jsonResponse } from "../../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string; mentalModelId: string }> };

function modelPath(bankId: string, mentalModelId: string): string {
  return bankScopedPath(
    "/v1/default/banks",
    bankId,
    `/mental-models/${encodeURIComponent(mentalModelId)}`,
  );
}

/** Per-mental-model sub-resource: GET reads, PATCH updates. */
export async function GET(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const { bankId, mentalModelId } = await context.params;
  return proxyUpstream({
    config,
    request,
    upstreamPath: modelPath(bankId, mentalModelId),
    query: searchParamsObject(request),
    method: "GET",
  });
}

export async function PATCH(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const { bankId, mentalModelId } = await context.params;
  const body = await request.text();
  return proxyUpstream({
    config,
    request,
    upstreamPath: modelPath(bankId, mentalModelId),
    method: "PATCH",
    body,
  });
}

/**
 * Deleting a mental model returns a small success payload rather than the
 * upstream delete body.
 */
export async function DELETE(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const { bankId, mentalModelId } = await context.params;
  const response = await proxyUpstream({
    config,
    request,
    upstreamPath: modelPath(bankId, mentalModelId),
    method: "DELETE",
  });
  if (!response.ok) return response;
  return jsonResponse({ success: true });
}