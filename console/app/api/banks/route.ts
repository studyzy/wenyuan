import { resolveServerConfig } from "../../../lib/server/config";
import { proxyUpstream, searchParamsObject } from "../../../lib/server/proxy";

/** Bank collection: GET lists banks, POST creates a bank. */
export async function GET(request: Request): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  return proxyUpstream({
    config,
    request,
    upstreamPath: "/v1/default/banks",
    query: searchParamsObject(request),
    method: "GET",
  });
}

export async function POST(request: Request): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  return proxyUpstream({
    config,
    request,
    upstreamPath: "/v1/default/banks",
    method: "POST",
    body,
  });
}