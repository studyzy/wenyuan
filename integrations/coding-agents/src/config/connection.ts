import { existsSync } from "node:fs";
import { delimiter } from "node:path";
import { readStoredConfig, writeStoredConfig } from "./store";

/**
 * Connection parameters: how the packaged runtime reaches the memory service.
 *
 * Resolution order for every parameter is fixed: an explicit flag, then the
 * stored configuration, then the deployment environment override, then the
 * built-in default. An already-stored service mode or address short-circuits
 * the chain entirely, so a working install is never reconfigured behind the
 * operator's back and nothing ever prompts without a terminal.
 */

export const SERVER_MODES = ["hosted", "self-hosted", "local"] as const;
export type ServerMode = (typeof SERVER_MODES)[number];

/** The stored configuration's keys, exactly five and no more. */
export const CONFIG_KEYS = {
  serverMode: "serverMode",
  apiUrl: "apiUrl",
  apiToken: "apiToken",
  bankId: "bankId",
  apiPort: "apiPort",
} as const;

/** Deployment contract: the environment overrides a deployment already sets. */
export const DEPLOYMENT_ENV = {
  apiUrl: "WENYUAN_API_URL",
  apiToken: "WENYUAN_API_TOKEN",
  bankId: "WENYUAN_BANK_ID",
  apiPort: "WENYUAN_API_PORT",
} as const;

/** Built-in defaults, kept together so they are easy to retarget. */
export const DEFAULT_SERVER_MODE: ServerMode = "self-hosted";
export const DEFAULT_API_HOST = "127.0.0.1";
export const DEFAULT_API_PORT = 18888;
export const DEFAULT_API_URL = `http://${DEFAULT_API_HOST}:${DEFAULT_API_PORT}`;
export const DEFAULT_BANK_ID = "default";

export interface ConnectionFlags {
  server?: string;
  apiUrl?: string;
  apiToken?: string;
}

export interface ResolvedConnection {
  serverMode: ServerMode;
  apiUrl: string;
  apiToken?: string;
  bankId: string;
  apiPort: number;
  /** Advisory only: a locally managed service with gaps still installs. */
  warnings: string[];
}

export type ConnectionResolution =
  | { ok: true; connection: ResolvedConnection }
  | { ok: false; message: string };

function firstNonEmpty(...values: (string | undefined)[]): string | undefined {
  for (const value of values) {
    if (typeof value === "string" && value.trim().length > 0) {
      return value.trim();
    }
  }
  return undefined;
}

function asServerMode(value: unknown): ServerMode | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  const normalized: string = value.trim();
  return SERVER_MODES.find((mode) => mode === normalized);
}

function asPort(value: unknown, fallback: number): number {
  const parsed: number = typeof value === "number" ? value : Number.parseInt(String(value ?? ""), 10);
  return Number.isInteger(parsed) && parsed > 0 && parsed < 65536 ? parsed : fallback;
}

function derivedApiUrl(port: number): string {
  return `http://${DEFAULT_API_HOST}:${port}`;
}

function environmentValue(environment: NodeJS.ProcessEnv, key: string): string | undefined {
  return firstNonEmpty(environment[key]);
}

function hasExecutable(name: string, environment: NodeJS.ProcessEnv): boolean {
  const searchPath: string | undefined = environment.PATH;
  if (!searchPath) {
    return false;
  }
  return searchPath
    .split(delimiter)
    .some((directory) => directory.length > 0 && isExecutablePresent(directory, name));
}

function isExecutablePresent(directory: string, name: string): boolean {
  // The path is only inspected, never executed: absence is what matters.
  const candidate: string = `${directory}/${name}`;
  return existsSync(candidate) || existsSync(`${candidate}.exe`);
}

/**
 * Gaps a locally managed service can have. They are advisory: the install
 * completes and the operator starts the service before the first session.
 */
export function localModeWarnings(port: number, environment: NodeJS.ProcessEnv = process.env): string[] {
  const warnings: string[] = [];
  if (!hasExecutable("wenyuan", environment) && !hasExecutable("docker", environment)) {
    warnings.push(
      `nothing on this machine looks like the local service yet: no "wenyuan" binary and no "docker" on PATH, so start one listening on port ${port} before your first session.`,
    );
  }
  return warnings;
}

/**
 * The environment block the packaged runtime reads, so the deployment
 * environment-variable names are spelled in exactly one place.
 */
export function runtimeEnvironment(connection: ResolvedConnection): Record<string, string> {
  const environment: Record<string, string> = { [DEPLOYMENT_ENV.bankId]: connection.bankId };
  if (connection.apiUrl.length > 0) {
    environment[DEPLOYMENT_ENV.apiUrl] = connection.apiUrl;
  }
  if (connection.apiToken) {
    environment[DEPLOYMENT_ENV.apiToken] = connection.apiToken;
  }
  if (connection.serverMode === "local") {
    environment[DEPLOYMENT_ENV.apiPort] = String(connection.apiPort);
  }
  return environment;
}

/**
 * Resolves and persists the connection parameters. Validation happens before
 * any write, so a rejected run leaves the stored configuration and every host
 * config exactly as they were.
 */
export async function resolveConnection(
  flags: ConnectionFlags = {},
  environment: NodeJS.ProcessEnv = process.env,
): Promise<ConnectionResolution> {
  const modeFlag: string | undefined = firstNonEmpty(flags.server);
  if (modeFlag !== undefined && !asServerMode(modeFlag)) {
    return {
      ok: false,
      message: `unrecognized server mode "${modeFlag}"; accepted values: ${SERVER_MODES.join(", ")}`,
    };
  }

  const stored: Record<string, unknown> = await readStoredConfig();
  const storedMode: ServerMode | undefined = asServerMode(stored[CONFIG_KEYS.serverMode]);
  const storedUrl: string | undefined = firstNonEmpty(
    typeof stored[CONFIG_KEYS.apiUrl] === "string" ? (stored[CONFIG_KEYS.apiUrl] as string) : undefined,
  );
  const storedToken: string | undefined = firstNonEmpty(
    typeof stored[CONFIG_KEYS.apiToken] === "string" ? (stored[CONFIG_KEYS.apiToken] as string) : undefined,
  );
  const storedBank: string | undefined = firstNonEmpty(
    typeof stored[CONFIG_KEYS.bankId] === "string" ? (stored[CONFIG_KEYS.bankId] as string) : undefined,
  );
  const storedPort: number | undefined =
    stored[CONFIG_KEYS.apiPort] === undefined
      ? undefined
      : asPort(stored[CONFIG_KEYS.apiPort], DEFAULT_API_PORT);
  const envUrl: string | undefined = environmentValue(environment, DEPLOYMENT_ENV.apiUrl);
  const envToken: string | undefined = environmentValue(environment, DEPLOYMENT_ENV.apiToken);
  const envBank: string | undefined = environmentValue(environment, DEPLOYMENT_ENV.bankId);
  const envPort: number = asPort(environmentValue(environment, DEPLOYMENT_ENV.apiPort), DEFAULT_API_PORT);
  // Stored before environment, matching the fixed precedence chain.
  const apiPort: number = storedPort ?? envPort;

  const missingTokenMessage =
    `hosted mode needs an auth token: pass --api-token <token>, or set ${DEPLOYMENT_ENV.apiToken}.`;
  const missingUrlMessage =
    `self-hosted mode needs a service address: pass --api-url <url>, or set ${DEPLOYMENT_ENV.apiUrl}.`;

  // An already-stored configuration short-circuits: no prompt, no rewrite.
  if (!modeFlag && !flags.apiUrl && !flags.apiToken && storedMode) {
    const apiUrl: string =
      storedMode === "local" ? derivedApiUrl(apiPort) : (storedUrl ?? envUrl ?? DEFAULT_API_URL);
    const apiToken: string | undefined = storedToken ?? envToken;
    if (storedMode === "hosted" && !apiToken) {
      return { ok: false, message: missingTokenMessage };
    }
    return {
      ok: true,
      connection: {
        serverMode: storedMode,
        apiUrl,
        apiToken,
        bankId: storedBank ?? envBank ?? DEFAULT_BANK_ID,
        apiPort,
        warnings: storedMode === "local" ? localModeWarnings(apiPort, environment) : [],
      },
    };
  }

  const serverMode: ServerMode = asServerMode(modeFlag) ?? storedMode ?? DEFAULT_SERVER_MODE;
  const apiToken: string | undefined = firstNonEmpty(flags.apiToken) ?? storedToken ?? envToken;
  const bankId: string = storedBank ?? envBank ?? DEFAULT_BANK_ID;

  if (serverMode === "hosted" && !apiToken) {
    return { ok: false, message: missingTokenMessage };
  }

  let apiUrl: string;
  if (serverMode === "local") {
    // The address is derived from the port, so no explicit one is stored.
    apiUrl = derivedApiUrl(apiPort);
  } else {
    apiUrl = firstNonEmpty(flags.apiUrl) ?? storedUrl ?? envUrl ?? DEFAULT_API_URL;
    // An explicitly requested self-hosted install never silently falls back
    // to another server, so a missing address is refused rather than guessed.
    if (asServerMode(modeFlag) === "self-hosted" && !firstNonEmpty(flags.apiUrl) && !storedUrl && !envUrl) {
      return { ok: false, message: missingUrlMessage };
    }
  }

  await writeStoredConfig({
    [CONFIG_KEYS.serverMode]: serverMode,
    [CONFIG_KEYS.apiUrl]: serverMode === "local" ? undefined : apiUrl,
    [CONFIG_KEYS.apiToken]: apiToken,
    [CONFIG_KEYS.bankId]: bankId,
    [CONFIG_KEYS.apiPort]: serverMode === "local" ? apiPort : undefined,
  });

  return {
    ok: true,
    connection: {
      serverMode,
      apiUrl,
      apiToken,
      bankId,
      apiPort,
      warnings: serverMode === "local" ? localModeWarnings(apiPort, environment) : [],
    },
  };
}
