import { apiDelete, apiGet, apiPatch, apiPost } from "./api";

/** Typed client functions for each console API route the pages consume. */
export type Bank = { bank_id: string; name?: string; created_at?: string };
export type BankList = { banks: Bank[] };

/**
 * The resolved bank configuration.
 *
 * The three disposition traits are graded 1..5 with 3 neutral; the reflection
 * mission is free text. Both the effective values and the bank-specific
 * overrides are returned, so a reader can tell a configured value from an
 * inherited one.
 */
export type BankConfig = {
  config: {
    disposition_skepticism: number;
    disposition_literalism: number;
    disposition_empathy: number;
    reflect_mission: string;
  };
  overrides: {
    disposition_skepticism?: number;
    disposition_literalism?: number;
    disposition_empathy?: number;
    reflect_mission?: string;
  };
};

/** The overridable configuration keys, and the bounds the traits are graded on. */
export const DISPOSITION_TRAITS = [
  "disposition_skepticism",
  "disposition_literalism",
  "disposition_empathy",
] as const;
export const DISPOSITION_MIN = 1;
export const DISPOSITION_MAX = 5;
export const DISPOSITION_NEUTRAL = 3;
/** The reflection mission is the one non-numeric overridable key. */
export const REFLECT_MISSION_KEY = "reflect_mission";

export const banksApi = {
  list: () => apiGet<BankList>("/api/banks"),
  get: (bankId: string) => apiGet<Bank>(`/api/banks/${encodeURIComponent(bankId)}`),
  create: (payload: unknown) => apiPost<Bank>("/api/banks", payload),
  update: (bankId: string, payload: unknown) =>
    apiPatch<Bank>(`/api/banks/${encodeURIComponent(bankId)}`, payload),
  remove: (bankId: string) => apiDelete<void>(`/api/banks/${encodeURIComponent(bankId)}`),
  aliases: (bankId: string) => apiGet<unknown>(`/api/banks/${encodeURIComponent(bankId)}/aliases`),
  config: (bankId: string) => apiGet<BankConfig>(`/api/banks/${encodeURIComponent(bankId)}/config`),
  /** Updates travel as an updates object, which is the shape the route reads. */
  updateConfig: (bankId: string, updates: Record<string, number | string>) =>
    apiPatch<BankConfig>(`/api/banks/${encodeURIComponent(bankId)}/config`, { updates }),
  resetConfig: (bankId: string) =>
    apiDelete<BankConfig>(`/api/banks/${encodeURIComponent(bankId)}/config`),
  webhooks: (bankId: string) => apiGet<unknown>(`/api/banks/${encodeURIComponent(bankId)}/webhooks`),
  mentalModels: (bankId: string) =>
    apiGet<unknown>(`/api/banks/${encodeURIComponent(bankId)}/mental-models`),
  observations: (bankId: string) =>
    apiGet<unknown>(`/api/banks/${encodeURIComponent(bankId)}/observations`),
};

export const memoriesApi = {
  list: (bankId: string) => apiGet<unknown>("/api/list", { bank_id: bankId }),
  retain: (payload: unknown) => apiPost<unknown>("/api/memories/retain", payload),
  retainAsync: (payload: unknown) => apiPost<unknown>("/api/memories/retain_async", payload),
  /** The memory detail read is bank-scoped, so the bank id travels with the id. */
  get: (bankId: string, memoryId: string) =>
    apiGet<unknown>(`/api/memories/${encodeURIComponent(memoryId)}`, { bank_id: bankId }),
  /** Curation and edits go through the same update; the bank id routes it. */
  update: (bankId: string, memoryId: string, payload: unknown) =>
    apiPatch<unknown>(`/api/memories/${encodeURIComponent(memoryId)}`, {
      bank_id: bankId,
      ...(payload as Record<string, unknown>),
    }),
  history: (bankId: string, memoryId: string) =>
    apiGet<unknown>(`/api/memories/${encodeURIComponent(memoryId)}/history`, { bank_id: bankId }),
};

export const documentsApi = {
  /** The collection is bank-scoped; document_id narrows it to one document. */
  list: (bankId: string) => apiGet<unknown>("/api/documents", { bank_id: bankId }),
  get: (documentId: string, bankId: string) =>
    apiGet<unknown>("/api/documents", { bank_id: bankId, document_id: documentId }),
  chunks: (documentId: string, bankId: string) =>
    apiGet<unknown>("/api/documents/chunks", { bank_id: bankId, document_id: documentId }),
};

export const operationsApi = {
  get: (bankId: string) => apiGet<unknown>(`/api/operations/${encodeURIComponent(bankId)}`),
  remove: (bankId: string, operationId: string) =>
    apiDelete<unknown>(`/api/operations/${encodeURIComponent(bankId)}`, { operation_id: operationId }),
};

export const recallApi = {
  run: (payload: unknown) => apiPost<unknown>("/api/recall", payload),
};

export const reflectApi = {
  run: (payload: unknown) => apiPost<unknown>("/api/reflect", payload),
};

export const statsApi = {
  get: (bankId: string) => apiGet<unknown>(`/api/stats/${encodeURIComponent(bankId)}`),
};

export const knowledgeBaseApi = {
  tree: (bankId: string) => apiGet<unknown>("/api/knowledge-base/tree", { bank_id: bankId }),
  node: (nodeId: string) => apiGet<unknown>(`/api/knowledge-base/nodes/${encodeURIComponent(nodeId)}`),
  pages: (bankId: string) => apiGet<unknown>("/api/knowledge-base/pages", { bank_id: bankId }),
  search: (query: string) => apiGet<unknown>("/api/knowledge-base/search", { q: query }),
  folders: (bankId: string) => apiGet<unknown>("/api/knowledge-base/folders", { bank_id: bankId }),
  export: (bankId: string) => apiGet<unknown>("/api/knowledge-base/export", { bank_id: bankId }),
  /** The mental models derived from this bank, which the models tab lists. */
  models: (bankId: string) => apiGet<unknown>("/api/knowledge-base/models", { bank_id: bankId }),
};
export const versionApi = {
  get: () => apiGet<{ version?: string; service?: string; features?: Record<string, boolean> }>("/api/version"),
};

export const entitiesApi = {
  list: (bankId: string) => apiGet<unknown>("/api/entities", { bank_id: bankId }),
  get: (entityId: string, bankId: string) =>
    apiGet<unknown>(`/api/entities/${encodeURIComponent(entityId)}`, { bank_id: bankId }),
  graph: (bankId: string) => apiGet<unknown>("/api/entities/graph", { bank_id: bankId }),
};

export const graphApi = {
  get: (bankId: string) => apiGet<unknown>("/api/graph", { bank_id: bankId }),
};

export const chunksApi = {
  get: (chunkId: string, _bankId?: string) => apiGet<unknown>("/api/chunks", { chunk_id: chunkId }),
  /** A memory's version history is served by the memory-history route. */
  history: (bankId: string, memoryId: string) =>
    apiGet<unknown>(`/api/memories/${encodeURIComponent(memoryId)}/history`, { bank_id: bankId }),
};

export const statsApiExtra = {
  timeseries: (bankId: string) => apiGet<unknown>(`/api/stats/${encodeURIComponent(bankId)}/memories-timeseries`),
};

export const auditLogsApi = {
  list: (bankId: string, query?: Record<string, string | undefined>) =>
    apiGet<unknown>(`/api/banks/${encodeURIComponent(bankId)}/audit-logs`, query),
  stats: (bankId: string) =>
    apiGet<unknown>(`/api/banks/${encodeURIComponent(bankId)}/audit-logs/stats`),
};

export const llmRequestsApi = {
  list: (bankId: string, query?: Record<string, string | undefined>) =>
    apiGet<unknown>(`/api/banks/${encodeURIComponent(bankId)}/llm-requests`, query),
  stats: (bankId: string) =>
    apiGet<unknown>(`/api/banks/${encodeURIComponent(bankId)}/llm-requests/stats`),
};

export const directivesApi = {
  list: (bankId: string) =>
    apiGet<unknown>(`/api/banks/${encodeURIComponent(bankId)}/directives`),
  create: (bankId: string, payload: unknown) =>
    apiPost<unknown>(`/api/banks/${encodeURIComponent(bankId)}/directives`, payload),
  update: (bankId: string, directiveId: string, payload: unknown) =>
    apiPatch<unknown>(
      `/api/banks/${encodeURIComponent(bankId)}/directives/${encodeURIComponent(directiveId)}`,
      payload,
    ),
  remove: (bankId: string, directiveId: string) =>
    apiDelete<unknown>(
      `/api/banks/${encodeURIComponent(bankId)}/directives/${encodeURIComponent(directiveId)}`,
    ),
};

export const tagsApi = {
  list: (bankId: string) => apiGet<unknown>(`/api/banks/${encodeURIComponent(bankId)}/tags`),
};

export const attachmentsApi = {
  url: (bankId: string, attachmentId: string) =>
    `/api/banks/${encodeURIComponent(bankId)}/attachments/${encodeURIComponent(attachmentId)}`,
};

export const webhooksApi = {
  list: (bankId: string) => apiGet<unknown>(`/api/banks/${encodeURIComponent(bankId)}/webhooks`),
  create: (bankId: string, payload: unknown) =>
    apiPost<unknown>(`/api/banks/${encodeURIComponent(bankId)}/webhooks`, payload),
  update: (bankId: string, webhookId: string, payload: unknown) =>
    apiPatch<unknown>(
      `/api/banks/${encodeURIComponent(bankId)}/webhooks/${encodeURIComponent(webhookId)}`,
      payload,
    ),
  remove: (bankId: string, webhookId: string) =>
    apiDelete<unknown>(
      `/api/banks/${encodeURIComponent(bankId)}/webhooks/${encodeURIComponent(webhookId)}`,
    ),
  deliveries: (bankId: string, webhookId: string, query?: Record<string, string | undefined>) =>
    apiGet<unknown>(
      `/api/banks/${encodeURIComponent(bankId)}/webhooks/${encodeURIComponent(webhookId)}/deliveries`,
      query,
    ),
};

export const mentalModelsApi = {
  list: (bankId: string) =>
    apiGet<unknown>(`/api/banks/${encodeURIComponent(bankId)}/mental-models`),
  create: (bankId: string, payload: unknown) =>
    apiPost<unknown>(`/api/banks/${encodeURIComponent(bankId)}/mental-models`, payload),
  get: (bankId: string, modelId: string) =>
    apiGet<unknown>(
      `/api/banks/${encodeURIComponent(bankId)}/mental-models/${encodeURIComponent(modelId)}`,
    ),
  update: (bankId: string, modelId: string, payload: unknown) =>
    apiPatch<unknown>(
      `/api/banks/${encodeURIComponent(bankId)}/mental-models/${encodeURIComponent(modelId)}`,
      payload,
    ),
  remove: (bankId: string, modelId: string) =>
    apiDelete<unknown>(
      `/api/banks/${encodeURIComponent(bankId)}/mental-models/${encodeURIComponent(modelId)}`,
    ),
  refresh: (bankId: string, modelId: string) =>
    apiPost<unknown>(
      `/api/banks/${encodeURIComponent(bankId)}/mental-models/${encodeURIComponent(modelId)}/refresh`,
    ),
  clear: (bankId: string, modelId: string) =>
    apiPost<unknown>(
      `/api/banks/${encodeURIComponent(bankId)}/mental-models/${encodeURIComponent(modelId)}/clear`,
    ),
  history: (bankId: string, modelId: string) =>
    apiGet<unknown>(
      `/api/banks/${encodeURIComponent(bankId)}/mental-models/${encodeURIComponent(modelId)}/history`,
    ),
};

export const observationsApi = {
  list: (bankId: string) => apiGet<unknown>(`/api/banks/${encodeURIComponent(bankId)}/observations`),
  clear: (bankId: string) =>
    apiDelete<unknown>(`/api/banks/${encodeURIComponent(bankId)}/observations`),
  scopes: (bankId: string) =>
    apiGet<unknown>(`/api/banks/${encodeURIComponent(bankId)}/observations/scopes`),
};
