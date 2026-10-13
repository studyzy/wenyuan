import { resolveServerConfig, type ServerConfig } from "../../../../lib/server/config";
import { requestLocale, searchParamsObject } from "../../../../lib/server/proxy";
import { errorResponse } from "../../../../lib/server/http";
import { buildUpstreamUrl, callUpstream, upstreamHeaders } from "../../../../lib/server/upstream";

/** Fixed poll interval and bounded overall deadline for the export orchestration. */
const POLL_INTERVAL_MS = 1_000;
const EXPORT_TIMEOUT_MS = 60_000;

type ExportSubmission = { operation_id?: unknown; id?: unknown };

function operationIdFrom(payload: ArrayBuffer): string | undefined {
  try {
    const decoded = JSON.parse(new TextDecoder().decode(payload)) as ExportSubmission;
    if (typeof decoded.operation_id === "string") return decoded.operation_id;
    if (typeof decoded.id === "string") return decoded.id;
  } catch {
    // Unparseable submission payload: treated as a failed export.
  }
  return undefined;
}

type OperationState = { status?: unknown; result?: unknown };

function archivePathFrom(payload: ArrayBuffer): string | undefined {
  try {
    const decoded = JSON.parse(new TextDecoder().decode(payload)) as OperationState;
    const result = decoded.result as Record<string, unknown> | undefined;
    const candidate = result?.["download_url"] ?? result?.["path"] ?? decoded.result;
    if (typeof candidate === "string" && candidate.startsWith("/")) return candidate;
  } catch {
    // Unparseable operation payload: keep polling until the deadline.
  }
  return undefined;
}

function isTerminal(payload: ArrayBuffer): boolean {
  try {
    const decoded = JSON.parse(new TextDecoder().decode(payload)) as OperationState;
    return decoded.status === "completed" || decoded.status === "succeeded";
  } catch {
    return false;
  }
}

function isFailed(payload: ArrayBuffer): boolean {
  try {
    const decoded = JSON.parse(new TextDecoder().decode(payload)) as OperationState;
    return decoded.status === "failed" || decoded.status === "error";
  } catch {
    return false;
  }
}

/**
 * Document transfer export.
 *
 * The upstream asynchronous export is hidden from the caller: the export is
 * submitted, its operation is polled at a fixed interval up to a bounded
 * overall deadline, then the finished archive is streamed back as a single
 * binary response. The success status is the upstream status, not a surrogate.
 */
export async function GET(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const query = searchParamsObject(request);

  const submitted = await callUpstream(config, {
    path: "/v1/default/documents/transfer",
    method: "POST",
    query,
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ ...query, mode: "export" }),
  });
  if (!submitted.ok) return passthroughResult(submitted);

  const operationId = operationIdFrom(submitted.body);
  if (!operationId) return errorResponse("api.errors.generic.internal", 500, locale);

  const deadline = Date.now() + EXPORT_TIMEOUT_MS;
  while (Date.now() < deadline) {
    await delay(POLL_INTERVAL_MS);
    const operation = await callUpstream(config, {
      path: `/v1/default/documents/transfer/${encodeURIComponent(operationId)}`,
      method: "GET",
    });
    if (isFailed(operation.body)) return errorResponse("api.errors.generic.internal", 500, locale);
    if (isTerminal(operation.body)) {
      const archivePath = archivePathFrom(operation.body);
      if (!archivePath) return errorResponse("api.errors.generic.internal", 500, locale);
      const archive = await fetchBinary(config, archivePath);
      return archive;
    }
  }
  return errorResponse("api.errors.generic.internal", 500, locale);
}

/** Document transfer import. */
export async function POST(request: Request): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.arrayBuffer();
  const result = await callUpstream(config, {
    path: "/v1/default/documents/transfer",
    method: "POST",
    query: searchParamsObject(request),
    headers: { "content-type": request.headers.get("content-type") ?? "application/octet-stream" },
    body,
  });
  return passthroughResult(result);
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function passthroughResult(result: {
  status: number;
  contentType: string | null;
  contentDisposition: string | null;
  body: ArrayBuffer;
}): Response {
  const headers = new Headers();
  if (result.contentType) headers.set("content-type", result.contentType);
  if (result.contentDisposition) headers.set("content-disposition", result.contentDisposition);
  return new Response(result.body, { status: result.status, headers });
}

async function fetchBinary(config: ServerConfig, path: string): Promise<Response> {
  const response = await fetch(buildUpstreamUrl(config, { path }), {
    method: "GET",
    headers: upstreamHeaders(config),
  });
  const headers = new Headers();
  headers.set("content-type", response.headers.get("content-type") ?? "application/octet-stream");
  return new Response(await response.arrayBuffer(), { status: response.status, headers });
}