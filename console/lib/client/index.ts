/**
 * Public entrypoint for the browser-side API access layer.
 */
export { publicBasePath, apiUrl, apiRequest, apiGet, apiPost, apiPatch, apiDelete } from "./api";
export type { ApiError } from "./api";
export {
  banksApi,
  memoriesApi,
  documentsApi,
  operationsApi,
  recallApi,
  reflectApi,
  statsApi,
  knowledgeBaseApi,
  type Bank,
  type BankList,
} from "./endpoints";
export {
  createBankSelection,
  selectBank,
  clearBankSelection,
  fetchVersion,
  readFeatureFlags,
  signIn,
  signOut,
  type BankSelection,
  type FeatureFlags,
  type VersionPayload,
} from "./state";