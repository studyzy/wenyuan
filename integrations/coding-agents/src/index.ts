/**
 * The package's library entry point: the seams a consumer or a later slice
 * builds on, and nothing else. The installer's own behavior lives in the
 * modules this re-exports.
 */

export { OWNERSHIP_MARKER, carriesMarker, valueCarriesMarker } from "./write/marker";
export {
  containerAt,
  mergeEntryIntoContainer,
  mergeValueIntoContainer,
  removeOwnEntriesFromContainer,
  removeOwnValuesFromContainer,
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

export { mcpServerPath, runtimeDirectory } from "./runtime/layout";
export { stageRuntime, builtRuntimeDirectory, runtimeIsStaged } from "./runtime/stage";
export { MemoryClient, openMemoryClient, textsFromRecall } from "./runtime/memory-client";
export { resolveHookConnection, type HookConnection, type HookConnectionResult } from "./runtime/hook-connection";
export { hooksDisabled, failOpen, readStdin, parsePayload, lifecyclePayload, DISABLE_HOOKS_ENV } from "./runtime/hook-contract";
export { stageDeadlineMs, STOP_DEADLINE_MARGIN_SECONDS } from "./runtime/deadline";
export { runSessionStart } from "./runtime/session-start-stage";
export { runPrePrompt } from "./runtime/pre-prompt-stage";
export { runStop } from "./runtime/stop-stage";
export { conversationFromPayload, conversationDocumentId, conversationFingerprint } from "./runtime/conversation";

export { HOOKS_CONTAINER_KEY, installLifecycleHooks, hookPathFor } from "./hosts/hooks";
export { removeLifecycleHooks } from "./hosts/hooks-remove";
export {
  HOOK_COMMAND_TYPE,
  MATCHER_GROUP_KEY,
  encodeRegistration,
  commandEntriesOf,
  registrationIsOwned,
} from "./hosts/hook-shape";
export {
  HOST_LIFECYCLES,
  HOOK_SCRIPT_FILES,
  hostLifecycle,
  declaredHostIds,
  orderedRegistrations,
  registrationFor,
  stageForScriptFile,
  budgetMilliseconds,
} from "./hosts/lifecycle";
export { LIFECYCLE_STAGES } from "./hosts/lifecycle-types";
export type {
  LifecycleRegistration,
  LifecycleStage,
  HostLifecycle,
  BudgetUnit,
  RegistrationStyle,
} from "./hosts/lifecycle-types";
export {
  MCP_HARNESS_ENV,
  MCP_SERVER_NAME,
  MCP_LAUNCHER_COMMAND,
  MCP_SERVERS_KEY,
  MCP_TOML_CONTAINER_KEY,
  MCP_ENTRY_MEMBERS,
  MECHANISMS,
  JSON_HOST_CANDIDATES,
  DSH_HOST_PATH,
  TOML_HOST_PATH,
  mechanismFor,
  mechanismKindFor,
  locationResolutionFor,
  containerKeyFor,
  declaredMechanismHosts,
} from "./mcp/mechanisms";
export type {
  MechanismKind,
  EntryMemberNames,
  LocationResolution,
  McpMechanismDeclaration,
} from "./mcp/mechanisms-types";
export {
  classifyEntry,
  entryIsOwned,
  manualRegistrationLine,
} from "./mcp/ownership";
export type { McpEntryInput, McpOwnership, McpResult, McpServerEntry } from "./mcp/ownership";
export { inspectMcpServer, registerMcpServer, removeMcpServer } from "./mcp/register";
export {
  registerJsonServerMap,
  removeJsonServerMap,
  candidatePaths,
  resolveCandidate,
} from "./mcp/json-server-map";
export { registerTomlTable, removeTomlTable, applyTomlEdit, stripOurTable } from "./mcp/toml-table";
export {
  registerCommandLine,
  removeCommandLine,
  addArguments,
  removeArguments,
} from "./mcp/command-line";
export { copyPackagedSkill, removeInstalledSkill, packagedSkillDirectory, SKILL_DIRECTORY_NAME } from "./hosts/skills";
export { skillsRootFor, installedSkillDirectoryFor } from "./hosts/skill-locations";
export { SKILL_NAME, SKILL_DESCRIPTION, SKILL_TOOL_IDENTIFIERS } from "./hosts/skill-tools";
export { refreshInstalledSkill, type SkillRefreshOutcome } from "./runtime/skill-refresh";

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
