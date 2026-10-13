import { resolveServerConfig } from "../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../lib/server/proxy";
import { errorResponse } from "../../../lib/server/http";

const PASSTHROUGH_FIELDS = [
  "tags",
  "tags_match",
  "tag_groups",
  "apply_all_directives",
  "max_tokens",
  "fact_types",
  "exclude_mental_models",
  "exclude_mental_model_ids",
  "response_schema",
  "reflect_search_observations_max_tokens",
  "reflect_search_observations_include_entities",
] as const;

/**
 * Reflect.
 *
 * The legacy include booleans are translated into an include object with facts
 * and tool_calls sub-objects. The budget defaults to a mid value when a legacy
 * thinking budget is present and no budget was given, and to a low value
 * otherwise. Other optional fields are forwarded only when truthy. Returns 200
 * with the reflect payload.
 */
export async function POST(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const bankId = searchParams.get("bank_id") ?? searchParams.get("agent_id");
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const raw = await request.text();
  let parsed: Record<string, unknown>;
  try {
    parsed = JSON.parse(raw) as Record<string, unknown>;
  } catch {
    return errorResponse("api.errors.auth.invalidRequestBody", 400, locale);
  }

  const includeOptions: Record<string, unknown> = {};
  if (parsed.include_facts) includeOptions.facts = {};
  if (parsed.include_tool_calls) includeOptions.tool_calls = {};

  const requestBody: Record<string, unknown> = {
    query: parsed.query,
    budget: parsed.budget ?? (parsed.thinking_budget ? "mid" : "low"),
  };
  for (const field of PASSTHROUGH_FIELDS) {
    if (parsed[field]) requestBody[field] = parsed[field];
  }
  if (Object.keys(includeOptions).length > 0) requestBody.include = includeOptions;

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "reflect_failed",
    errorKey: "api.errors.reflect.failed",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/reflect"),
      method: "POST",
      body: JSON.stringify(requestBody),
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
