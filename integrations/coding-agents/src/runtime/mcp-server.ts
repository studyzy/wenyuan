import { openMemoryClient, type MemoryClient } from "./memory-client";

/**
 * The packaged MCP server's transport.
 *
 * A host launches the server over standard input/output and speaks JSON-RPC on
 * it, so this module owns the protocol side: one JSON object per line in, one
 * JSON object per line out, nothing ever written to standard output off the
 * wire. The memory operations themselves come from the same client the hooks
 * use, so the server and the hooks cannot disagree about how a request is
 * shaped or which bank it targets.
 *
 * The tool surface is deliberately left to the slice that owns it: this server
 * answers the handshake and lists no tools yet, and the one read path it does
 * serve is a recall. That keeps a launched server well-behaved and lets the
 * registration be verified end to end before the tool set is filled in.
 */

/** The protocol revision a handshake may request; the newest is answered. */
export const SUPPORTED_PROTOCOL_VERSIONS: readonly string[] = ["2024-11-05", "2025-03-26", "2025-06-18"];

/** The server's own name, reported in the handshake. */
export const SERVER_NAME = "wenyuan-memory";

/** The server's own version, reported in the handshake. */
export const SERVER_VERSION = "0.1.0";

/** The one tool this slice serves. */
export const RECALL_TOOL_NAME = "wenyuan_recall";

/** The recall tool's declaration, written in the MCP tool schema shape. */
export const RECALL_TOOL = {
  name: RECALL_TOOL_NAME,
  description: "Recall what WenYuan memory already knows about this project and the user.",
  inputSchema: {
    type: "object",
    properties: {
      query: { type: "string", description: "What to recall." },
    },
    required: ["query"],
  },
} as const;

/** The two members a host passes on the launcher's argv, if any. */
export interface ServerArguments {
  transport?: string;
}

/**
 * Reads the launcher's own arguments. A host writes the server path first and
 * may append its own flags; an unknown flag is ignored rather than refused, so
 * a host's evolving argv never stops the server from starting.
 */
export function parseServerArguments(argv: readonly string[]): ServerArguments {
  const parsed: ServerArguments = {};
  for (let index = 0; index < argv.length; index += 1) {
    const argument: string = argv[index] ?? "";
    if (argument === "--transport") {
      parsed.transport = argv[index + 1];
      index += 1;
    }
  }
  return parsed;
}

/** One JSON-RPC request or notification, as much of it as the server reads. */
interface RpcMessage {
  jsonrpc?: string;
  id?: string | number | null;
  method?: string;
  params?: unknown;
}

/** A JSON-RPC response the server writes back, or null for a notification. */
type RpcReply = Record<string, unknown> | null;

function reply(id: RpcMessage["id"], result: unknown): Record<string, unknown> {
  return { jsonrpc: "2.0", id: id ?? null, result };
}

function failure(id: RpcMessage["id"], code: number, message: string): Record<string, unknown> {
  return { jsonrpc: "2.0", id: id ?? null, error: { code, message } };
}

/** The text content block an MCP tool result carries. */
function textResult(text: string): Record<string, unknown> {
  return { content: [{ type: "text", text }] };
}

/**
 * One request, answered. Unknown methods are an error (MCP requires it) while
 * notifications — requests with no id — are acknowledged by writing nothing.
 */
export async function handleMessage(
  message: RpcMessage,
  client: MemoryClient | null,
): Promise<RpcReply> {
  if (message.id === undefined || message.id === null) {
    return null;
  }
  switch (message.method) {
    case "initialize": {
      const requested: unknown = (message.params as Record<string, unknown> | undefined)?.["protocolVersion"];
      const version: string =
        typeof requested === "string" && SUPPORTED_PROTOCOL_VERSIONS.includes(requested)
          ? requested
          : SUPPORTED_PROTOCOL_VERSIONS[SUPPORTED_PROTOCOL_VERSIONS.length - 1]!;
      return reply(message.id, {
        protocolVersion: version,
        capabilities: { tools: {} },
        serverInfo: { name: SERVER_NAME, version: SERVER_VERSION },
      });
    }
    case "ping":
      return reply(message.id, {});
    case "tools/list":
      return reply(message.id, { tools: [RECALL_TOOL] });
    case "tools/call": {
      const params: Record<string, unknown> = (message.params as Record<string, unknown>) ?? {};
      if (params["name"] !== RECALL_TOOL_NAME) {
        return reply(message.id, {
          content: [{ type: "text", text: `no tool named ${String(params["name"] ?? "")}` }],
          isError: true,
        });
      }
      const query: unknown = (params["arguments"] as Record<string, unknown> | undefined)?.["query"];
      if (typeof query !== "string" || query.trim().length === 0) {
        return reply(message.id, textResult("no query was given"));
      }
      if (client === null) {
        return reply(message.id, textResult("the memory service is not reachable"));
      }
      const outcome = await client.recall(query).catch(() => ({ ok: false as const, texts: [] as string[] }));
      if (!outcome.ok) {
        return reply(message.id, textResult("the memory service could not be reached"));
      }
      return reply(message.id, textResult(outcome.texts.length === 0 ? "no memory matched" : outcome.texts.join("\n")));
    }
    default:
      return failure(message.id, -32601, `method not found: ${String(message.method ?? "")}`);
  }
}

/** Splits a stream of newline-delimited JSON documents into parsed messages. */
export async function* readMessages(
  input: NodeJS.ReadableStream,
): AsyncGenerator<RpcMessage, void, void> {
  let buffer = "";
  for await (const chunk of input as AsyncIterable<Buffer | string>) {
    buffer += typeof chunk === "string" ? chunk : chunk.toString("utf8");
    let newline: number = buffer.indexOf("\n");
    while (newline !== -1) {
      const line: string = buffer.slice(0, newline).trim();
      buffer = buffer.slice(newline + 1);
      if (line.length > 0) {
        try {
          const parsed: unknown = JSON.parse(line);
          if (parsed !== null && typeof parsed === "object" && !Array.isArray(parsed)) {
            yield parsed as RpcMessage;
          }
        } catch {
          // A malformed line is not a request; it is dropped rather than
          // answered, because the line carries no id to answer to.
        }
      }
      newline = buffer.indexOf("\n");
    }
  }
  // A host may close the stream after a final document with no trailing
  // newline; that document is still a request and is answered like any other.
  const trailing: string = buffer.trim();
  if (trailing.length > 0) {
    try {
      const parsed: unknown = JSON.parse(trailing);
      if (parsed !== null && typeof parsed === "object" && !Array.isArray(parsed)) {
        yield parsed as RpcMessage;
      }
    } catch {
      // A trailing fragment that is not a document carries no id to answer to.
    }
  }
}

export interface ServerOptions {
  arguments: ServerArguments;
  input?: NodeJS.ReadableStream;
  output?: NodeJS.WritableStream;
  /** Injectable for tests; the packaged connection is opened when absent. */
  client?: MemoryClient | null;
  /** Injectable for tests; the real environment is used when absent. */
  environment?: NodeJS.ProcessEnv;
}

/**
 * Serves the protocol until the host closes the stream, then returns. It never
 * blocks on anything but its own input and never writes to standard output
 * except the one JSON line per answered request.
 */
export async function launchStdioServer(options: ServerOptions): Promise<void> {
  const input: NodeJS.ReadableStream = options.input ?? process.stdin;
  const output: NodeJS.WritableStream = options.output ?? process.stdout;
  const client: MemoryClient | null =
    options.client !== undefined
      ? options.client
      : await openMemoryClient(10_000, options.environment ?? process.env).catch(() => null);
  for await (const message of readMessages(input)) {
    const answer: RpcReply = await handleMessage(message, client).catch((reason: unknown) =>
      failure(message.id, -32603, reason instanceof Error ? reason.message : "internal error"),
    );
    if (answer !== null) {
      output.write(`${JSON.stringify(answer)}\n`);
    }
  }
}