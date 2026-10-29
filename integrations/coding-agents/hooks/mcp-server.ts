import { launchStdioServer, parseServerArguments } from "../src/runtime/mcp-server";

/**
 * The packaged MCP server.
 *
 * A host launches it over standard input/output and resolves nothing else, so
 * it is self-contained: it reads the connection it should use from its
 * environment (the host's server entry injects those members), serves the
 * memory tools over the stdio transport, and exits quietly when the host
 * closes the stream.
 */

await launchStdioServer({ arguments: parseServerArguments(process.argv.slice(2)) });