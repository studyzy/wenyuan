/**
 * Public entrypoint for shared server-side building blocks.
 */
export {
  ACCESS_KEY_ENV,
  DATAPLANE_URL_ENV,
  DATAPLANE_KEY_ENV,
  BASE_PATH_ENV,
  MAX_UPLOAD_SIZE_ENV,
  DEFAULT_DATAPLANE_URL,
  DEFAULT_MAX_UPLOAD_SIZE,
  resolveServerConfig,
  isGateEnabled,
  type ServerConfig,
  type EnvSource,
} from "./config";
export {
  prefixBasePath,
  stripBasePath,
  normalizeBasePath,
  encodeBankId,
  sanitizeReturnTo,
} from "./paths";
export { localizeError, buildErrorBody, ERROR_KEY_FIELD, MESSAGE_FIELD_PRIORITY } from "./errors";
export {
  SESSION_COOKIE,
  SESSION_MAX_AGE_SECONDS,
  createSessionToken,
  verifySessionToken,
  verifyAccessKey,
  isSecureRequest,
  sessionCookieAttributes,
  clearSessionCookieAttributes,
} from "./session";
export {
  buildUpstreamUrl,
  upstreamHeaders,
  callUpstream,
  passthrough,
  type UpstreamResult,
  type UpstreamRequest,
} from "./upstream";
export {
  jsonResponse,
  errorResponse,
  requiredQueryParam,
  booleanQueryFlags,
  resolveBankScoping,
  routeParam,
  type RouteContext,
} from "./http";
export {
  ATTACHMENT_ID_LENGTH,
  ATTACHMENT_ID_PATTERN,
  FALLBACK_BINARY_CONTENT_TYPE,
  isAttachmentId,
} from "./attachment";