import { resolveServerConfig } from "../../../../../../lib/server/config";
import { bankScopedPath, proxyUpstream } from "../../../../../../lib/server/proxy";

type Context = { params: Promise<{ bankId: string }> };

/** Prompt preview for the prompt tester. */
export async function POST(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  return proxyUpstream({
    config,
    request,
    upstreamPath: bankScopedPath("/v1/default/banks", (await context.params).bankId, "/prompts/preview"),
    method: "POST",
    body,
  });
}