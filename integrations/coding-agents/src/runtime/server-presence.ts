import { MCP_SERVER_FILE_NAME } from "../runtime/layout";
import { builtRuntimeDirectory } from "../runtime/stage";

/**
 * The packaged MCP server the MCP registration hands a host.
 *
 * Every mechanism writes an entry whose argv names the staged server file, so
 * install must be able to guarantee that file exists before it writes anything:
 * a registration pointing at a missing script is a host that silently exposes
 * no memory tools. The check is deliberately a plain file test, and the path it
 * tests is the one a host's entry resolves through.
 */

/** True when the packaged server file is present in the runtime directory. */
export async function stagedServerFileExists(runtimeDirectory: string): Promise<boolean> {
  const { stat } = await import("node:fs/promises");
  try {
    return (await stat(joinServerFile(runtimeDirectory))).isFile();
  } catch {
    return false;
  }
}

function joinServerFile(runtimeDirectory: string): string {
  return `${runtimeDirectory}/${MCP_SERVER_FILE_NAME}`;
}

/**
 * The reason install cannot wire a host, or null when the packaged server is
 * staged. It names the expected file so an operator can see what is missing
 * rather than reading a host config that points at nothing.
 */
export async function packagedServerReason(runtimeDirectory: string): Promise<string | null> {
  if (await stagedServerFileExists(runtimeDirectory)) {
    return null;
  }
  const source: string | null = builtRuntimeDirectory();
  return (
    `the packaged MCP server is missing: ${joinServerFile(runtimeDirectory)} was not staged` +
    (source === null
      ? ", and this package ships no built runtime — run the package's build script first"
      : ` from ${source}; run the package's build script first`)
  );
}