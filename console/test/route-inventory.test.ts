import { describe, expect, it } from "vitest";

import * as authLogin from "../app/api/auth/login/route";
import * as authLogout from "../app/api/auth/logout/route";
import * as banks from "../app/api/banks/route";
import * as bankDetail from "../app/api/banks/[bankId]/route";
import * as aliases from "../app/api/banks/[bankId]/aliases/route";
import * as aliasDetail from "../app/api/banks/[bankId]/aliases/[alias]/route";
import * as attachment from "../app/api/banks/[bankId]/attachments/[attachmentId]/route";
import * as auditLogs from "../app/api/banks/[bankId]/audit-logs/route";
import * as auditStats from "../app/api/banks/[bankId]/audit-logs/stats/route";
import * as clone from "../app/api/banks/[bankId]/clone/route";
import * as bankConfig from "../app/api/banks/[bankId]/config/route";
import * as consolidate from "../app/api/banks/[bankId]/consolidate/route";
import * as recover from "../app/api/banks/[bankId]/consolidation-recover/route";
import * as strategyPreview from "../app/api/banks/[bankId]/consolidation-strategies/preview/route";
import * as directives from "../app/api/banks/[bankId]/directives/route";
import * as directiveDetail from "../app/api/banks/[bankId]/directives/[directiveId]/route";
import * as bankExport from "../app/api/banks/[bankId]/export/route";
import * as llmHealth from "../app/api/banks/[bankId]/health/llm/route";
import * as bankImport from "../app/api/banks/[bankId]/import/route";
import * as llmRequests from "../app/api/banks/[bankId]/llm-requests/route";
import * as llmStats from "../app/api/banks/[bankId]/llm-requests/stats/route";
import * as dryRunExtract from "../app/api/banks/[bankId]/memories/dry-run-extract/route";
import * as mentalModels from "../app/api/banks/[bankId]/mental-models/route";
import * as mentalModelDetail from "../app/api/banks/[bankId]/mental-models/[mentalModelId]/route";
import * as mmClear from "../app/api/banks/[bankId]/mental-models/[mentalModelId]/clear/route";
import * as mmDryRun from "../app/api/banks/[bankId]/mental-models/[mentalModelId]/dry-run-refresh/route";
import * as mmHistory from "../app/api/banks/[bankId]/mental-models/[mentalModelId]/history/route";
import * as mmRefresh from "../app/api/banks/[bankId]/mental-models/[mentalModelId]/refresh/route";
import * as observations from "../app/api/banks/[bankId]/observations/route";
import * as observationDetail from "../app/api/banks/[bankId]/observations/[modelId]/route";
import * as observationScopes from "../app/api/banks/[bankId]/observations/scopes/route";
import * as bankOperation from "../app/api/banks/[bankId]/operations/[operationId]/route";
import * as promptPreview from "../app/api/banks/[bankId]/prompts/preview/route";
import * as tags from "../app/api/banks/[bankId]/tags/route";
import * as webhooks from "../app/api/banks/[bankId]/webhooks/route";
import * as webhookDetail from "../app/api/banks/[bankId]/webhooks/[webhookId]/route";
import * as deliveries from "../app/api/banks/[bankId]/webhooks/[webhookId]/deliveries/route";
import * as documents from "../app/api/documents/route";
import * as documentChunks from "../app/api/documents/chunks/route";
import * as documentReprocess from "../app/api/documents/reprocess/route";
import * as documentTransfer from "../app/api/documents/transfer/route";
import * as entities from "../app/api/entities/route";
import * as entityDetail from "../app/api/entities/[entityId]/route";
import * as entityRegenerate from "../app/api/entities/[entityId]/regenerate/route";
import * as entityGraph from "../app/api/entities/graph/route";
import * as graph from "../app/api/graph/route";
import * as kbTree from "../app/api/knowledge-base/tree/route";
import * as kbNodes from "../app/api/knowledge-base/nodes/[nodeId]/route";
import * as kbPages from "../app/api/knowledge-base/pages/route";
import * as kbPageDetail from "../app/api/knowledge-base/pages/[pageId]/route";
import * as kbSearch from "../app/api/knowledge-base/search/route";
import * as kbFolders from "../app/api/knowledge-base/folders/route";
import * as kbExport from "../app/api/knowledge-base/export/route";
import * as list from "../app/api/list/route";
import * as retain from "../app/api/memories/retain/route";
import * as retainAsync from "../app/api/memories/retain_async/route";
import * as memoryDetail from "../app/api/memories/[memoryId]/route";
import * as memoryHistory from "../app/api/memories/[memoryId]/history/route";
import * as operations from "../app/api/operations/[agentId]/route";
import * as recall from "../app/api/recall/route";
import * as reflect from "../app/api/reflect/route";
import * as stats from "../app/api/stats/[agentId]/route";
import * as timeseries from "../app/api/stats/[agentId]/memories-timeseries/route";
import * as chunks from "../app/api/chunks/route";
import * as filesRetain from "../app/api/files/retain/route";
import * as filesDownload from "../app/api/files/download/route";
import * as health from "../app/api/health/route";
import * as version from "../app/api/version/route";

type RouteModule = Record<string, unknown>;
const HTTP_METHODS: readonly string[] = ["GET", "POST", "PUT", "PATCH", "DELETE"];

function asModule(value: unknown): RouteModule {
  return value as RouteModule;
}

function boundMethods(value: unknown): string[] {
  const mod = asModule(value);
  return HTTP_METHODS.filter((method) => typeof mod[method] === "function").sort();
}

/**
 * The full per-route HTTP method set from the approved spec's method-set claim.
 * Every control-plane API route appears exactly once.
 */
const INVENTORY: Array<[string, unknown, string[]]> = [
  ["/api/auth/login", authLogin, ["POST"]],
  ["/api/auth/logout", authLogout, ["POST"]],
  ["/api/banks", banks, ["GET", "POST"]],
  ["/api/banks/[bankId]", bankDetail, ["DELETE", "PATCH", "PUT"]],
  ["/api/banks/[bankId]/aliases", aliases, ["GET", "POST"]],
  ["/api/banks/[bankId]/aliases/[alias]", aliasDetail, ["DELETE", "PATCH"]],
  ["/api/banks/[bankId]/attachments/[attachmentId]", attachment, ["GET"]],
  ["/api/banks/[bankId]/audit-logs", auditLogs, ["GET"]],
  ["/api/banks/[bankId]/audit-logs/stats", auditStats, ["GET"]],
  ["/api/banks/[bankId]/clone", clone, ["POST"]],
  ["/api/banks/[bankId]/config", bankConfig, ["DELETE", "GET", "PATCH"]],
  ["/api/banks/[bankId]/consolidate", consolidate, ["POST"]],
  ["/api/banks/[bankId]/consolidation-recover", recover, ["POST"]],
  ["/api/banks/[bankId]/consolidation-strategies/preview", strategyPreview, ["POST"]],
  ["/api/banks/[bankId]/directives", directives, ["GET", "POST"]],
  ["/api/banks/[bankId]/directives/[directiveId]", directiveDetail, ["DELETE", "GET", "PATCH"]],
  ["/api/banks/[bankId]/export", bankExport, ["GET"]],
  ["/api/banks/[bankId]/health/llm", llmHealth, ["POST"]],
  ["/api/banks/[bankId]/import", bankImport, ["POST"]],
  ["/api/banks/[bankId]/llm-requests", llmRequests, ["GET"]],
  ["/api/banks/[bankId]/llm-requests/stats", llmStats, ["GET"]],
  ["/api/banks/[bankId]/memories/dry-run-extract", dryRunExtract, ["POST"]],
  ["/api/banks/[bankId]/mental-models", mentalModels, ["GET", "POST"]],
  ["/api/banks/[bankId]/mental-models/[mentalModelId]", mentalModelDetail, ["DELETE", "GET", "PATCH"]],
  ["/api/banks/[bankId]/mental-models/[mentalModelId]/clear", mmClear, ["POST"]],
  ["/api/banks/[bankId]/mental-models/[mentalModelId]/dry-run-refresh", mmDryRun, ["POST"]],
  ["/api/banks/[bankId]/mental-models/[mentalModelId]/history", mmHistory, ["GET"]],
  ["/api/banks/[bankId]/mental-models/[mentalModelId]/refresh", mmRefresh, ["POST"]],
  ["/api/banks/[bankId]/observations", observations, ["DELETE", "GET"]],
  ["/api/banks/[bankId]/observations/[modelId]", observationDetail, ["GET"]],
  ["/api/banks/[bankId]/observations/scopes", observationScopes, ["GET"]],
  ["/api/banks/[bankId]/operations/[operationId]", bankOperation, ["DELETE", "GET", "POST"]],
  ["/api/banks/[bankId]/prompts/preview", promptPreview, ["POST"]],
  ["/api/banks/[bankId]/tags", tags, ["GET"]],
  ["/api/banks/[bankId]/webhooks", webhooks, ["GET", "POST"]],
  ["/api/banks/[bankId]/webhooks/[webhookId]", webhookDetail, ["DELETE", "PATCH"]],
  ["/api/banks/[bankId]/webhooks/[webhookId]/deliveries", deliveries, ["GET"]],
  ["/api/documents", documents, ["DELETE", "GET", "PATCH"]],
  ["/api/documents/chunks", documentChunks, ["GET"]],
  ["/api/documents/reprocess", documentReprocess, ["POST"]],
  ["/api/documents/transfer", documentTransfer, ["GET", "POST"]],
  ["/api/entities", entities, ["GET"]],
  ["/api/entities/[entityId]", entityDetail, ["GET"]],
  ["/api/entities/[entityId]/regenerate", entityRegenerate, ["POST"]],
  ["/api/entities/graph", entityGraph, ["GET"]],
  ["/api/graph", graph, ["GET"]],
  ["/api/knowledge-base/tree", kbTree, ["GET"]],
  ["/api/knowledge-base/nodes/[nodeId]", kbNodes, ["DELETE", "PATCH"]],
  ["/api/knowledge-base/pages", kbPages, ["POST"]],
  ["/api/knowledge-base/pages/[pageId]", kbPageDetail, ["GET"]],
  ["/api/knowledge-base/search", kbSearch, ["GET"]],
  ["/api/knowledge-base/folders", kbFolders, ["POST"]],
  ["/api/knowledge-base/export", kbExport, ["GET"]],
  ["/api/list", list, ["DELETE", "GET"]],
  ["/api/memories/retain", retain, ["POST"]],
  ["/api/memories/retain_async", retainAsync, ["POST"]],
  ["/api/memories/[memoryId]", memoryDetail, ["GET", "PATCH"]],
  ["/api/memories/[memoryId]/history", memoryHistory, ["GET"]],
  ["/api/operations/[agentId]", operations, ["DELETE", "GET"]],
  ["/api/recall", recall, ["POST"]],
  ["/api/reflect", reflect, ["POST"]],
  ["/api/stats/[agentId]", stats, ["GET"]],
  ["/api/stats/[agentId]/memories-timeseries", timeseries, ["GET"]],
  ["/api/chunks", chunks, ["GET"]],
  ["/api/files/retain", filesRetain, ["POST"]],
  ["/api/files/download", filesDownload, ["GET"]],
  ["/api/health", health, ["GET"]],
  ["/api/version", version, ["GET"]],
];

describe("control-plane route inventory", () => {
  it("materializes exactly the 68 catalogued routes", () => {
    expect(INVENTORY).toHaveLength(68);
    expect(new Set(INVENTORY.map(([path]) => path)).size).toBe(68);
  });

  it("includes a bank-scoped child sub-resource and a nested action beneath it", () => {
    const paths = INVENTORY.map(([path]) => path);
    expect(paths).toContain("/api/banks/[bankId]/mental-models/[mentalModelId]");
    expect(paths).toContain("/api/banks/[bankId]/mental-models/[mentalModelId]/history");
  });
});

describe("control-plane route method sets", () => {
  for (const [path, mod, expected] of INVENTORY) {
    it(`${path} binds exactly ${expected.join("/")}`, () => {
      expect(boundMethods(mod)).toEqual([...expected].sort());
    });
  }

  it("binds no method handler outside the five HTTP methods", () => {
    for (const [path, mod] of INVENTORY) {
      for (const key of Object.keys(asModule(mod))) {
        expect(HTTP_METHODS, `${path} exports ${key}`).toContain(key);
      }
    }
  });
});