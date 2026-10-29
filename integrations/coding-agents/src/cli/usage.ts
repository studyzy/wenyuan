import { hostNames } from "../hosts/registry";
import { ALL_HOSTS_KEYWORD } from "../hosts/select";
import { COMMANDS } from "./argv";

/**
 * The one usage text.
 *
 * The host list it prints is the same list host selection validates against, so
 * help and validation cannot drift apart.
 */

export function usageText(): string {
  const hosts: string = hostNames().join(", ");
  return [
    "wenyuan-agents — wire the WenYuan memory service into coding-agent hosts.",
    "",
    `Usage: wenyuan-agents <${COMMANDS.join("|")}> [host ...] [flags]`,
    "",
    "Commands:",
    "  install <host ...>     merge the three lifecycle hooks, write the MCP server entry",
    "                         and copy the companion skill, for each selected host; a host",
    "                         whose wiring is a plugin (dsh) gets that plugin installed",
    "                         through the host's own command instead of hooks",
    "  uninstall <host ...>   remove exactly what install wrote, leaving foreign entries alone",
    "  update                 re-stage the packaged runtime only; it writes no host config",
    "                         and takes no host argument",
    "  stats                  print the recorded usage report; reads a local log, writes nothing",
    "",
    `Hosts: ${hosts}`,
    `  pass "${ALL_HOSTS_KEYWORD}" instead of a name to wire every host detected on this machine`,
    "",
    "Flags:",
    "  --server <mode>            hosted | self-hosted | local",
    "  --api-url <url>            service address, required by the self-hosted mode",
    "  --api-token <token>        bearer token, required by the hosted mode",
    "  --import-conversations     additionally attempt the past-session backfill",
    "  --enable-workspace-mcp     authorize flipping a host's global workspace-MCP setting",
    "",
    "Every long flag is also accepted in the equals-joined spelling, for example",
    "--server=local. Connection parameters resolve in one fixed order: an explicit",
    "flag, then the stored configuration, then the deployment environment override,",
    "then the built-in default. Non-interactive runs are never prompted: they take",
    "the documented default and log where to change it.",
    "",
  ].join("\n");
}
