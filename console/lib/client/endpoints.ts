import { apiDelete, apiGet, apiPatch, apiPost } from "./api";

/** Typed client functions for each console API route the pages consume. */
export type Bank = { bank_id: string; name?: string; created_at?: string };
export type BankList = { banks: Bank[] };

export const banksApi = {
  list: () => apiGet<BankList>("/api/banks"),
  get: (bankId: string) => apiGet<Bank>(`/api/banks/${encodeURIComponent(bankId)}`),
  create: (payload: unknown) => apiPost<Bank>("/api/banks", payload),
  update: (bankId: string, payload: unknown) =>
    apiPatch<Bank>(`/api/banks/${encodeURIComponent(bankId)}`, payload),
  remove: (bankId: string) => apiDelete<void>(`/api/banks/${encodeURIComponent(bankId)}`),
  aliases: (bankId: string) => apiGet<unknown>(`/api/banks/${encodeURIComponent(bankId)}/aliases`),
  config: (bankId: string) => apiGet<unknown>(`/api/banks/${encodeURIComponent(bankId)}/config`),
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
  get: (memoryId: string) => apiGet<unknown>(`/api/memories/${encodeURIComponent(memoryId)}`),
};

export const documentsApi = {
  get: (documentId: string) => apiGet<unknown>("/api/documents", { document_id: documentId }),
  chunks: (documentId: string) =>
    apiGet<unknown>("/api/documents/chunks", { document_id: documentId }),
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
};