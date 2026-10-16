import { homeDirectory } from "../config/paths";
import { beginGroup, describeError, fail, say } from "../cli/log";
import { selectHosts } from "../hosts/select";
import type { HostContribution, HostEnvironment } from "../hosts/types";
import { runtimeDirectory } from "../runtime/layout";

/**
 * The uninstall command.
 *
 * It removes exactly what install wrote — the package's own hook entries, its
 * own MCP server entry and its own skill copy — for each selected host, and
 * leaves foreign entries, unrelated top-level keys and the staged runtime
 * alone. Hosts are handled in registry order and a failure on one does not stop
 * the others.
 */

export interface UninstallRequest {
  hostNames: readonly string[];
}

export async function uninstall(request: UninstallRequest): Promise<number> {
  const home: string = homeDirectory();
  const selection = selectHosts(request.hostNames, home);
  if (!selection.ok) {
    beginGroup("hosts");
    fail(selection.message);
    return 1;
  }

  const env: HostEnvironment = { home, runtimeDirectory: runtimeDirectory() };
  const failed: string[] = [];
  for (const host of selection.hosts) {
    beginGroup(host.id);
    try {
      const result = await host.uninstall(env);
      for (const message of result.messages) {
        say(message);
      }
      if (result.ok) {
        say(`removed ${host.displayName}`);
      } else {
        fail(`${host.displayName} was not unwired`);
        failed.push(host.id);
      }
    } catch (cause) {
      fail(describeError(cause));
      failed.push(host.id);
    }
  }

  if (failed.length > 0) {
    const names: string[] = selection.hosts
      .map((host: HostContribution) => host.id)
      .filter((id: string) => failed.includes(id));
    fail(`not uninstalled: ${names.join(", ")}`);
    return 1;
  }
  return 0;
}
