/**
 * Server-side environment resolution.
 *
 * The variable names are the deployment contract and must be used verbatim.
 * Defaults: upstream URL to the local loopback port, upstream key empty,
 * base path empty, max upload size defaulted.
 */
export const ACCESS_KEY_ENV = "HINDSIGHT_CP_ACCESS_KEY";
export const DATAPLANE_URL_ENV = "HINDSIGHT_CP_DATAPLANE_API_URL";
export const DATAPLANE_KEY_ENV = "HINDSIGHT_CP_DATAPLANE_API_KEY";
export const BASE_PATH_ENV = "NEXT_PUBLIC_BASE_PATH";
export const MAX_UPLOAD_SIZE_ENV = "HINDSIGHT_CP_MAX_UPLOAD_SIZE";

export const DEFAULT_DATAPLANE_URL = "http://127.0.0.1:8000";
export const DEFAULT_MAX_UPLOAD_SIZE = 25 * 1024 * 1024;

export type ServerConfig = {
  accessKey: string;
  dataplaneUrl: string;
  dataplaneKey: string;
  basePath: string;
  maxUploadSize: number;
};

export type EnvSource = Record<string, string | undefined>;

export function resolveServerConfig(env: EnvSource): ServerConfig {
  const basePathRaw = (env[BASE_PATH_ENV] ?? "").trim();
  const basePath = basePathRaw === "/" ? "" : basePathRaw.replace(/\/+$/, "");

  const maxUploadRaw = (env[MAX_UPLOAD_SIZE_ENV] ?? "").trim();
  const parsedMax = Number.parseInt(maxUploadRaw, 10);

  return {
    accessKey: (env[ACCESS_KEY_ENV] ?? "").trim(),
    dataplaneUrl: ((env[DATAPLANE_URL_ENV] ?? "").trim() || DEFAULT_DATAPLANE_URL).replace(
      /\/+$/,
      "",
    ),
    dataplaneKey: (env[DATAPLANE_KEY_ENV] ?? "").trim(),
    basePath,
    maxUploadSize:
      Number.isFinite(parsedMax) && parsedMax > 0 ? parsedMax : DEFAULT_MAX_UPLOAD_SIZE,
  };
}

export function isGateEnabled(config: ServerConfig): boolean {
  return config.accessKey.length > 0;
}