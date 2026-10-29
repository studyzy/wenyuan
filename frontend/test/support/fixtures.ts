import type { ServerConfig } from "../../lib/server/config";

/** Shared environment and config fixtures. */
export function configFixture(overrides: Partial<ServerConfig> = {}): ServerConfig {
  return {
    accessKey: "",
    dataplaneUrl: "http://127.0.0.1:8000",
    dataplaneKey: "",
    adminUrl: "http://127.0.0.1:18890",
    adminKey: "",
    basePath: "",
    maxUploadSize: 25 * 1024 * 1024,
    ...overrides,
  };
}

/**
 * Install an env map for the duration of `run`, restoring prior values after.
 */
export async function withEnv<T>(
  env: Record<string, string | undefined>,
  run: () => Promise<T> | T,
): Promise<T> {
  const previous = new Map<string, string | undefined>();
  for (const [key, value] of Object.entries(env)) {
    previous.set(key, process.env[key]);
    if (value === undefined) delete process.env[key];
    else process.env[key] = value;
  }
  try {
    return await run();
  } finally {
    for (const [key, value] of previous.entries()) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  }
}

/** Stub global fetch with a queue of responses, capturing requested URLs and bodies. */
export function stubFetch(
  responses: Array<{ status?: number; body?: unknown; contentType?: string; reject?: boolean }>,
): {
  calls: string[];
  bodies: Array<string | undefined>;
  methods: string[];
  headers: Array<HeadersInit | undefined>;
  restore: () => void;
} {
  const original = globalThis.fetch;
  const calls: string[] = [];
  const bodies: Array<string | undefined> = [];
  const methods: string[] = [];
  const headers: Array<HeadersInit | undefined> = [];
  let index = 0;

  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push(typeof input === "string" ? input : input.toString());
    methods.push((init?.method ?? "GET").toUpperCase());
    headers.push(init?.headers);
    const rawBody = init?.body;
    bodies.push(typeof rawBody === "string" ? rawBody : undefined);
    const current = responses[Math.min(index, responses.length - 1)];
    index += 1;
    if (current?.reject) throw new TypeError("fetch failed");
    const status = current?.status ?? 200;
    const contentType = current?.contentType ?? "application/json";
    const payload =
      typeof current?.body === "string" ? current.body : JSON.stringify(current?.body ?? {});
    return new Response(payload, { status, headers: { "content-type": contentType } });
  }) as typeof fetch;

  return { calls, bodies, methods, headers, restore: () => { globalThis.fetch = original; } };
}