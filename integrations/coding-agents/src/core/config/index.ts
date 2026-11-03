/**
 * The config module's public face. The pinned import path stays `core/config` — the split below
 * the barrel is an internal layout, not a contract.
 */
export type { AutoInject, Config, LoadOptions, RawConfig } from "./raw";
export type { ObservationScopes } from "./scopes";
export { DEFAULT_OBSERVATION_SCOPES, DEFAULT_PAGE_SEARCH_LIMIT } from "./scopes";
export {
  DEFAULT_INJECT_TIMEOUT_MS,
  DEFAULT_REFLECT_TIMEOUT_MS,
  DEFAULT_REFLECT_TOOL_TIMEOUT_MS,
  DEFAULT_SEED_LIMIT,
  resolveConfig,
} from "./resolve";
export { loadConfig, readEnvConfig } from "./layers";
export { applyBankConfig } from "./bank-override";
