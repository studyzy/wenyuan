import { setTimeout as sleep } from "node:timers/promises";

import { resolveServerConfig, type ServerConfig } from "../../../../lib/server/config";
import { bankScopedPath, requestLocale } from "../../../../lib/server/proxy";
import { errorResponse } from "../../../../lib/server/http";
import {
  type UpstreamResult,
  buildUpstreamUrl,
  callUpstream,
  passthrough,
  upstreamHeaders,
} from "../../../../lib/server/upstream";

/**
 * Poll interval and bounded overall deadline for the export orchestration.
 * The upstream export is asynchronous, so the route submits it, polls the
 * operation to completion and then streams the finished archive back — one zip
 * download from the caller's point of view.
 */
const EXPORT_POLL_INTERVAL_MS = 1_500;
const EXPORT_TIMEOUT_MS = 5 * 60 * 1000;

type OperationState = {
  status?: unknown;
  result_metadata?: { download_url?: unknown } | null;
};

/** The submitted export's operation id, if the acceptance payload carried one. */
function operationIdOf(result: UpstreamResult): string | undefined {
  try {
    const decoded = JSON.parse(new TextDecoder().decode(result.body)) as { operation_id?: unknown };
    return typeof decoded.operation_id === "string" ? decoded.operation_id : undefined;
  } catch {
    return undefined;
  }
}

function operationState(result: UpstreamResult): OperationState {
  try {
    return JSON.parse(new TextDecoder().decode(result.body)) as OperationState;
  } catch {
    return {};
  }
}

/** Stream the finished archive with the reference's zip content type. */
async function downloadArchive(config: ServerConfig, downloadUrl: string): Promise<Response> {
  const response = await fetch(buildUpstreamUrl(config, { path: downloadUrl }), {
    method: "GET",
    headers: upstreamHeaders(config),
  });
  if (!response.ok) {
    const body = await response.arrayBuffer();
    return new Response(body, {
      status: response.status,
      headers: {
        "content-type": response.headers.get("content-type") ?? "application/json",
      },
    });
  }
  const headers = new Headers({ "content-type": "application/zip" });
  headers.set(
    "content-disposition",
    response.headers.get("content-disposition") ?? 'attachment; filename="documents.zip"',
  );
  return new Response(await response.arrayBuffer(), { status: 200, headers });
}

/**
 * Export documents as a transfer ZIP archive.
 *
 * Orchestrates the upstream async export (submit, poll, download) and streams
 * the binary zip back. A subset export carries no bank-level sections, so the
 * bank config and history flags are set false when document ids are named.
 */
export async function GET(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const bankId = searchParams.get("bank_id");
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);

  const documentIds = searchParams.getAll("document_id");
  const query: Record<string, string | string[]> = {};
  if (documentIds.length > 0) query.document_id = documentIds;
  if (searchParams.get("include_observations") === "true") query.include_observations = "true";
  if (searchParams.get("include_knowledge_base") === "true") {
    query.include_knowledge_base = "true";
  }
  if (documentIds.length > 0) {
    query.include_bank_config = "false";
    query.include_history = "false";
  }

  try {
    const submitted = await callUpstream(config, {
      path: bankScopedPath("/v1/default/banks", bankId, "/transfer/export"),
      method: "POST",
      query,
    });
    if (!submitted.ok) return passthrough(submitted);

    const operationId = operationIdOf(submitted);
    if (!operationId) return errorResponse("api.errors.documents.export", 500, locale);

    const deadline = Date.now() + EXPORT_TIMEOUT_MS;
    for (;;) {
      const status = operationState(
        await callUpstream(config, {
          path: bankScopedPath(
            "/v1/default/banks",
            bankId,
            `/operations/${encodeURIComponent(operationId)}`,
          ),
        }),
      );
      if (status.status === "completed") {
        const downloadUrl = status.result_metadata?.download_url;
        if (typeof downloadUrl !== "string" || downloadUrl.length === 0) {
          return errorResponse("api.errors.documents.export", 500, locale);
        }
        return await downloadArchive(config, downloadUrl);
      }
      if (status.status === "failed" || status.status === "cancelled") {
        return errorResponse("api.errors.documents.export", 500, locale);
      }
      if (Date.now() >= deadline) {
        return errorResponse("api.errors.documents.export", 504, locale);
      }
      await sleep(EXPORT_POLL_INTERVAL_MS);
    }
  } catch {
    return errorResponse("api.errors.documents.export", 500, locale);
  }
}

/**
 * Import a transfer ZIP archive into a bank.
 *
 * Proxies the multipart upload to the bank's transfer import with mode=merge,
 * which folds the archive's documents into the bank the caller is looking at.
 */
export async function POST(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const bankId = searchParams.get("bank_id");
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const onConflict = searchParams.get("on_conflict") || "skip";
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);

  const form = await request.formData();
  const file = form.get("file");
  if (!(file instanceof Blob)) {
    return errorResponse("api.errors.validation.fileRequired", 400, locale);
  }

  const outbound = new FormData();
  outbound.append("file", file, file instanceof File ? file.name : "transfer.zip");

  try {
    const result = await callUpstream(config, {
      path: bankScopedPath("/v1/default/banks", bankId, "/transfer/import"),
      method: "POST",
      query: { mode: "merge", document_conflict: onConflict },
      body: outbound,
    });
    return passthrough(result);
  } catch {
    return errorResponse("api.errors.documents.import", 500, locale);
  }
}