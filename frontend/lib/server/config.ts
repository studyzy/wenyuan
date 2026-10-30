/**
 * Server-side environment resolution.
 *
 * The variable names are the deployment contract and must be used verbatim.
 * Defaults: upstream URL to the local loopback port, upstream key empty,
 * base path empty, max upload size defaulted.
 */
export const DATAPLANE_URL_ENV = "WENYUAN_CP_DATAPLANE_API_URL";
export const DATAPLANE_KEY_ENV = "WENYUAN_CP_DATAPLANE_API_KEY";
export const ADMIN_URL_ENV = "WENYUAN_CP_ADMIN_API_URL";
export const ADMIN_KEY_ENV = "WENYUAN_CP_ADMIN_API_KEY";
export const BASE_PATH_ENV = "NEXT_PUBLIC_BASE_PATH";
export const MAX_UPLOAD_SIZE_ENV = "WENYUAN_CP_MAX_UPLOAD_SIZE";

export const DEFAULT_DATAPLANE_URL = "http://127.0.0.1:8000";
// The management plane's default matches the wenyuan server's offset port.
export const DEFAULT_ADMIN_URL = "http://127.0.0.1:18890";
export const DEFAULT_MAX_UPLOAD_SIZE = 25 * 1024 * 1024;

export type ServerConfig = {
  accessKey: string;
  dataplaneUrl: string;
  dataplaneKey: string;
  adminUrl: string;
  adminKey: string;
  basePath: string;
  maxUploadSize: number;
};

export type EnvSource = Record<string, string | undefined>;

export function resolveServerConfig(env: EnvSource): ServerConfig {
  const basePathRaw = (env[BASE_PATH_ENV] ?? "").trim();
  const basePath = basePathRaw === "/" ? "" : basePathRaw.replace(/\/+$/, "");

  const maxUploadRaw = (env[MAX_UPLOAD_SIZE_ENV] ?? "").trim();
  const parsedMax = Number.parseInt(maxUploadRaw, 10);

  const dataplaneKey = (env[DATAPLANE_KEY_ENV] ?? "").trim();
  const adminKey = (env[ADMIN_KEY_ENV] ?? "").trim();

  return {
    // The console has no passphrase of its own: the secret that signs session
    // cookies is the operator key when the management plane is configured, and
    // the data key otherwise. Signing with a credential the deployment already
    // holds means there is no third key to set, rotate or leak, and rotating
    // either one invalidates outstanding sessions.
    accessKey: adminKey || dataplaneKey,
    dataplaneUrl: ((env[DATAPLANE_URL_ENV] ?? "").trim() || DEFAULT_DATAPLANE_URL).replace(
      /\/+$/,
      "",
    ),
    dataplaneKey,
    adminUrl: ((env[ADMIN_URL_ENV] ?? "").trim() || DEFAULT_ADMIN_URL).replace(/\/+$/, ""),
    adminKey,
    basePath,
    maxUploadSize:
      Number.isFinite(parsedMax) && parsedMax > 0 ? parsedMax : DEFAULT_MAX_UPLOAD_SIZE,
  };
}

export function isGateEnabled(config: ServerConfig): boolean {
  return config.accessKey.length > 0;
}

/**
 * Re-point the shared upstream client at the management plane. The data-plane
 * helpers keep their shape; only the base URL and credential change, so the
 * operator key never reaches the browser and every route stays on the same
 * server-side call path.
 */
export function adminServerConfig(config: ServerConfig): ServerConfig {
  return {
    ...config,
    dataplaneUrl: config.adminUrl,
    dataplaneKey: config.adminKey,
  };
}