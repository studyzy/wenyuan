import { pathToFileURL } from "node:url";
import { install } from "../commands/install";
import { stats } from "../commands/stats";
import { uninstall } from "../commands/uninstall";
import { update } from "../commands/update";
import { COMMANDS, isCommand, parseArgs } from "./argv";
import { describeError, fail, printUsage } from "./log";
import { usageText } from "./usage";

/**
 * The dispatcher.
 *
 * Four command words and nothing else. No word at all prints the usage text and
 * exits zero; an unrecognized word prints it too but exits non-zero. The
 * read-only command is handled before any staging or host work so it can never
 * write, and every failure the documented exit status names — an unknown
 * command, an unknown host name, a missing host selection, a blocked or failed
 * install, a missing connection parameter — comes back non-zero.
 */
export async function run(argv: readonly string[]): Promise<number> {
  const parsed = parseArgs(argv);
  if (parsed.command === undefined) {
    printUsage(usageText());
    return 0;
  }
  if (!isCommand(parsed.command)) {
    printUsage(usageText());
    fail(`unknown command "${parsed.command}"; expected one of: ${COMMANDS.join(", ")}.`);
    return 1;
  }

  try {
    switch (parsed.command) {
      case "stats":
        return await stats();
      case "update":
        return await update();
      case "install":
        return await install({
          hostNames: parsed.hosts,
          server: parsed.flags.server,
          apiUrl: parsed.flags.apiUrl,
          apiToken: parsed.flags.apiToken,
          importConversations: parsed.flags.importConversations,
          enableWorkspaceMcp: parsed.flags.enableWorkspaceMcp,
        });
      case "uninstall":
        return await uninstall({ hostNames: parsed.hosts });
      default:
        return 1;
    }
  } catch (cause) {
    fail(describeError(cause));
    return 1;
  }
}

// Only self-execute when this file is the one being run: the end-to-end suites
// import the same entry point in-process to drive it against a fake home.
const invoked: string | undefined = process.argv[1];
if (invoked !== undefined && import.meta.url === pathToFileURL(invoked).href) {
  process.exit(await run(process.argv.slice(2)));
}
