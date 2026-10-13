import { resolveServerConfig } from "../../../../lib/server/config";
import { proxyUpstream } from "../../../../lib/server/proxy";

/** Knowledge-base pages: POST creates/updates. */
export async function POST(request: Request): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  return proxyUpstream({
    config,
    request,
    upstreamPath: "/v1/default/knowledge-base/pages",
    method: "POST",
    body,
  });
}