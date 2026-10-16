/**
 * The package's library entry point: the seams a consumer or a later slice
 * builds on, and nothing else. The installer's own behavior lives in the
 * modules this re-exports.
 */

export { OWNERSHIP_MARKER, carriesMarker, valueCarriesMarker } from "./write/marker";
export {
  containerAt,
  mergeEntryIntoContainer,
  removeOwnEntriesFromContainer,
  readJsonFile,
  writeJsonFile,
  type ConfigFormat,
  type JsonDocument,
} from "./write/json-merge";
export { backupExistingFile, backupPathFor, BACKUP_SUFFIX } from "./write/backup";
export {
  insertIntoJsoncArray,
  removeFromJsoncArray,
  upsertJsoncMember,
  removeJsoncMember,
  type JsoncEdit,
} from "./write/jsonc-edit";

export { homeDirectory, storedConfigPath, usageLogPath } from "./config/paths";
export { readStoredConfig, writeStoredConfig } from "./config/store";
export {
  resolveConnection,
  runtimeEnvironment,
  localModeWarnings,
  CONFIG_KEYS,
  DEPLOYMENT_ENV,
  SERVER_MODES,
  DEFAULT_API_PORT,
  DEFAULT_API_URL,
  DEFAULT_BANK_ID,
  type ConnectionFlags,
  type ConnectionResolution,
  type ResolvedConnection,
  type ServerMode,
} from "./config/connection";

export { hookPath, mcpServerPath, runtimeDirectory } from "./runtime/layout";
export { stageRuntime, builtRuntimeDirectory, runtimeIsStaged } from "./runtime/stage";

export { LIFECYCLE_EVENTS, installLifecycleHooks, removeLifecycleHooks } from "./hosts/hooks";
export type { LifecycleEventId, LifecycleHookEntry } from "./hosts/hooks";
export {
  MCP_HARNESS_ENV,
  MCP_SERVER_NAME,
  inspectMcpServer,
  registerMcpServer,
  removeMcpServer,
} from "./mcp/register";
export { copyPackagedSkill, removeInstalledSkill, installedSkillDirectory } from "./hosts/skills";

export { HOSTS, HOST_IDS, hostNames, findHost, detectHosts, hostIsDetected } from "./hosts/registry";
export { selectHosts, ALL_HOSTS_KEYWORD } from "./hosts/select";
export type {
  HostContribution,
  HostEnvironment,
  HostId,
  HostInstallOptions,
  HostLocations,
  HostOperationResult,
} from "./hosts/types";
