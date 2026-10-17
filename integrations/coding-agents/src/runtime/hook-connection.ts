import { CONFIG_KEYS, DEFAULT_API_PORT, DEPLOYMENT_ENV, DEFAULT_BANK_ID } from "../config/connection";
import { readStoredConfig } from "../config/store";

/**
 * How a packaged hook reaches the memory service.
 *
 * A hook runs inside a host and has no flags and no terminal, so it resolves
 * the connection purely from the stored per-package configuration the
 * installer wrote, with the deployment environment as an override. It never
 * writes credentials anywhere else: the connection parameters stay in the
 * stored configuration, exactly where install put them.
 */

export interface HookConnection {
  apiUrl: string;
  bankId: string;
  apiToken?: string;
}

export type HookConnectionResult =
  | { ok: true; connection: HookConnection }
  | { ok: false; reason: string };

function firstNonEmpty(...values: (string | undefined)[]): string | undefined {
  for (const value of values) {
    if (typeof value === "string" && value.trim().length > 0) {
      return value.trim();
    }
  }
  return undefined;
}

function asString(value: unknown): string | undefined {
  return typeof value === "string" && value.trim().length > 0 ? value.trim() : undefined;
}

function derivedApiUrl(port: number): string {
  return `http://127.0.0.1:${port}`;
}

function asPort(value: unknown): number | undefined {
  const parsed: number = typeof value === "number" ? value : Number.parseInt(String(value ?? ""), 10);
  return Number.isInteger(parsed) && parsed > 0 && parsed < 65536 ? parsed : undefined;
}

/**
 * Resolves the connection a hook uses, from the stored configuration the
 * installer persisted plus the deployment environment. A missing or unreadable
 * configuration is a soft failure: the caller proceeds fail-open rather than
 * throwing, so this never rejects.
 */
export async function resolveHookConnection(
  environment: NodeJS.ProcessEnv = process.env,
): Promise<HookConnectionResult> {
  let stored: Record<string, unknown>;
  try {
    stored = await readStoredConfig();
  } catch {
    return { ok: false, reason: "the stored configuration could not be read" };
  }

  const mode: string | undefined = asString(stored[CONFIG_KEYS.serverMode]);
  const storedUrl: string | undefined = asString(stored[CONFIG_KEYS.apiUrl]);
  const storedPort: number | undefined = asPort(stored[CONFIG_KEYS.apiPort]);
  const envUrl: string | undefined = firstNonEmpty(environment[DEPLOYMENT_ENV.apiUrl]);
  const envPort: number | undefined = asPort(environment[DEPLOYMENT_ENV.apiPort]);
  const port: number = storedPort ?? envPort ?? DEFAULT_API_PORT;

  const apiUrl: string | undefined =
    mode === "local"
      ? derivedApiUrl(port)
      : firstNonEmpty(storedUrl, envUrl);
  if (!apiUrl) {
    return { ok: false, reason: "no service address is configured" };
  }

  const bankId: string =
    asString(stored[CONFIG_KEYS.bankId]) ?? firstNonEmpty(environment[DEPLOYMENT_ENV.bankId]) ?? DEFAULT_BANK_ID;
  const apiToken: string | undefined =
    asString(stored[CONFIG_KEYS.apiToken]) ?? firstNonEmpty(environment[DEPLOYMENT_ENV.apiToken]);

  return { ok: true, connection: { apiUrl, bankId, apiToken } };
}