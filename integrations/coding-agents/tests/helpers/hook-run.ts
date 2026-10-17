import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { spawn } from "node:child_process";
import { join } from "node:path";
import type { AddressInfo } from "node:net";
import { createFakeHome, type FakeHome } from "./fake-home";
import { CONFIG_KEYS } from "../../src/config/connection";

/**
 * End-to-end support for the packaged hook scripts.
 *
 * A hook is launched exactly as a host launches it: by absolute path, as a
 * child process, with the host's lifecycle payload on standard input and the
 * per-user locations redirected through the environment. A tiny in-process
 * service stands in for the memory service, so a suite can drive a whole
 * firing through the served surface without a database.
 */

/** One recorded request the stub service received. */
export interface RecordedRequest {
  method: string;
  path: string;
  headers: Record<string, string | string[] | undefined>;
  body: unknown;
}

export interface StubService {
  url: string;
  requests: RecordedRequest[];
  /** What the recall route answers with; each call shifts one entry. */
  recallResponses: unknown[];
  /** What the capture route answers with. */
  captureStatus: number;
  close(): Promise<void>;
}

function readBody(request: IncomingMessage): Promise<string> {
  return new Promise((resolve) => {
    const chunks: Buffer[] = [];
    request.on("data", (chunk: Buffer) => chunks.push(chunk));
    request.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
    request.on("error", () => resolve(""));
  });
}

function send(response: ServerResponse, status: number, body: unknown): void {
  const payload: string = body === undefined ? "" : JSON.stringify(body);
  response.writeHead(status, { "content-type": "application/json" });
  response.end(payload);
}

/** Starts a stub memory service on an ephemeral loopback port. */
export async function startStubService(): Promise<StubService> {
  const requests: RecordedRequest[] = [];
  const service: StubService = {
    url: "",
    requests,
    recallResponses: [],
    captureStatus: 200,
    async close(): Promise<void> {
      await new Promise<void>((resolve) => server.close(() => resolve()));
    },
  };

  const server: Server = createServer(async (request: IncomingMessage, response: ServerResponse) => {
    const raw: string = await readBody(request);
    let body: unknown = undefined;
    try {
      body = raw.length > 0 ? JSON.parse(raw) : undefined;
    } catch {
      body = raw;
    }
    requests.push({ method: request.method ?? "", path: request.url ?? "", headers: request.headers, body });

    const path: string = request.url ?? "";
    if (path.endsWith("/memories/recall")) {
      const next = service.recallResponses.shift();
      send(response, 200, next ?? { results: [] });
      return;
    }
    if (path.endsWith("/memories")) {
      if (service.captureStatus >= 400) {
        send(response, service.captureStatus, {});
        return;
      }
      send(response, 200, { id: "captured" });
      return;
    }
    send(response, 404, {});
  });

  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", () => resolve()));
  const address = server.address() as AddressInfo;
  service.url = `http://127.0.0.1:${address.port}`;
  return service;
}

export interface RunHookOptions {
  home: FakeHome;
  /** The packaged hook script file to launch. */
  scriptFile: string;
  /** The lifecycle payload written to the hook's standard input. */
  payload?: unknown;
  /** Extra environment, merged over the redirected home. */
  environment?: Record<string, string>;
}

export interface HookRun {
  status: number;
  stdout: string;
  stderr: string;
  /** The single JSON envelope the hook printed, or null when it printed none. */
  envelope(): unknown;
}

/** Launches a packaged hook script as a host would, and captures its output. */
export async function runHook(options: RunHookOptions): Promise<HookRun> {
  const script: string = join(options.home.packageRoot, "dist", "runtime", "hooks", options.scriptFile);
  const environment: NodeJS.ProcessEnv = options.home.env();
  Object.assign(environment, options.environment ?? {});
  const child = spawn(process.execPath, [script], { env: environment, stdio: ["pipe", "pipe", "pipe"] });

  let stdout = "";
  let stderr = "";
  child.stdout.on("data", (chunk: Buffer) => {
    stdout += chunk.toString("utf8");
  });
  child.stderr.on("data", (chunk: Buffer) => {
    stderr += chunk.toString("utf8");
  });
  if (options.payload !== undefined) {
    child.stdin.write(typeof options.payload === "string" ? options.payload : JSON.stringify(options.payload));
  }
  child.stdin.end();

  const status: number = await new Promise((resolve) => child.on("close", (code) => resolve(code ?? -1)));
  return {
    status,
    stdout,
    stderr,
    envelope(): unknown {
      const trimmed: string = stdout.trim();
      if (trimmed === "") {
        return null;
      }
      const lastLine: string = trimmed.split("\n").pop() ?? "";
      try {
        return JSON.parse(lastLine);
      } catch {
        return null;
      }
    },
  };
}

/** Seeds the stored configuration the hook resolves its connection from. */
export async function seedStoredConnection(home: FakeHome, url: string, bankId = "default"): Promise<void> {
  await home.write(
    ".wenyuan/config.json",
    `${JSON.stringify({ [CONFIG_KEYS.serverMode]: "self-hosted", [CONFIG_KEYS.apiUrl]: url, [CONFIG_KEYS.bankId]: bankId }, null, 2)}\n`,
  );
}

/** Creates a fake home with the built hooks reachable, and cleans it up. */
export async function withHookHome<T>(body: (home: FakeHome) => Promise<T>): Promise<T> {
  const home: FakeHome = await createFakeHome({ environment: { PATH: "" } });
  try {
    return await body(home);
  } finally {
    await home.cleanup();
  }
}