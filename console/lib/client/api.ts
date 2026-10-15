/**
 * Browser-facing API base path handling.
 *
 * Browser requests must be base-path prefixed when a public base path is
 * configured. The upstream credential is never handled here; authorization and
 * session state remain server-side.
 */
export { normalizeBasePath as publicBasePath } from "../base-path";
import { normalizeBasePath } from "../base-path";

export function apiUrl(path: string, query?: Record<string, string | undefined>): string {
  const base = normalizeBasePath();
  const normalized = path.startsWith("/") ? path : `/${path}`;
  const url = new URL(`${base}${normalized}`, "http://console.local");
  if (query) {
    for (const [key, value] of Object.entries(query)) {
      if (value !== undefined && value !== "") url.searchParams.set(key, value);
    }
  }
  return `${url.pathname}${url.search}`;
}

export type ApiError = {
  status: number;
  body: unknown;
};

export async function apiRequest<T>(
  path: string,
  init: RequestInit & { query?: Record<string, string | undefined> } = {},
): Promise<T> {
  const { query, ...rest } = init;
  const response = await fetch(apiUrl(path, query), {
    ...rest,
    headers: {
      "content-type": "application/json",
      ...(rest.headers ?? {}),
    },
  });
  const text = await response.text();
  const body = text.length > 0 ? JSON.parse(text) : null;
  if (!response.ok) {
    const error: ApiError = { status: response.status, body };
    throw error;
  }
  return body as T;
}

export function apiGet<T>(path: string, query?: Record<string, string | undefined>): Promise<T> {
  return apiRequest<T>(path, { method: "GET", query });
}

export function apiPost<T>(path: string, payload?: unknown): Promise<T> {
  return apiRequest<T>(path, { method: "POST", body: JSON.stringify(payload ?? {}) });
}

export function apiPatch<T>(path: string, payload?: unknown): Promise<T> {
  return apiRequest<T>(path, { method: "PATCH", body: JSON.stringify(payload ?? {}) });
}

export function apiDelete<T>(path: string, query?: Record<string, string | undefined>): Promise<T> {
  return apiRequest<T>(path, { method: "DELETE", query });
}