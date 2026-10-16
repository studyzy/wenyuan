import { runtimeEnvironment, resolveConnection } from "../config/connection";
import { homeDirectory } from "../config/paths";
import { beginGroup, describeError, fail, say, warn } from "../cli/log";
import { selectHosts } from "../hosts/select";
import type { HostContribution, HostEnvironment } from "../hosts/types";
import { stageRuntime } from "../runtime/stage";
import { runPreflights } from "./preflight";

/**
 * The install command.
 *
 * The order is what makes the result safe: host selection is validated first so
 * a bad name can never partially install, then the runtime is staged so the
 * first hook command a host config carries already points at the stable copy,
 * then each host's blocking preflight runs, then the connection parameters are
 * resolved, and only then are hosts wired in registry order. A host that is
 * blocked or fails does not stop the others; every host that was not installed
 * is named in a trailing summary and the run exits non-zero.
 */

export interface InstallRequest {
  hostNames: readonly string[];
  server?: string;
  apiUrl?: string;
  apiToken?: string;
  /** Attempt the past-session backfill as well. */
  importConversations: boolean;
  /** Authorize flipping a host's global workspace-MCP setting. */
  enableWorkspaceMcp: boolean;
}

export async function install(request: InstallRequest): Promise<number> {
  const home: string = homeDirectory();
  const selection = selectHosts(request.hostNames, home);
  if (!selection.ok) {
    beginGroup("hosts");
    fail(selection.message);
    return 1;
  }
  if (selection.detected.length > 0) {
    beginGroup("hosts");
    say(`detected hosts: ${selection.detected.join(", ")}`);
  }

  beginGroup("runtime");
  let staged;
  try {
    staged = await stageRuntime();
  } catch (cause) {
    fail(describeError(cause));
    return 1;
  }
  say(`staged ${staged.files.length} runtime file(s) at ${staged.directory}`);

  const hostEnvironment: HostEnvironment = { home, runtimeDirectory: staged.directory };
  const outcomes = await runPreflights(selection.hosts, hostEnvironment);
  const notInstalled: string[] = [];
  const wireable: HostContribution[] = [];
  for (const outcome of outcomes) {
    if (outcome.reason === null) {
      wireable.push(outcome.host);
      continue;
    }
    beginGroup(outcome.host.id);
    fail(`${outcome.host.displayName} was skipped: ${outcome.reason}`);
    notInstalled.push(outcome.host.id);
  }

  if (wireable.length === 0) {
    fail(`not installed: ${notInstalled.join(", ")}`);
    return 1;
  }

  beginGroup("server");
  const resolution = await resolveConnection({
    server: request.server,
    apiUrl: request.apiUrl,
    apiToken: request.apiToken,
  });
  if (!resolution.ok) {
    fail(resolution.message);
    return 1;
  }
  const connection = resolution.connection;
  say(`service mode: ${connection.serverMode}`);
  say(`service address: ${connection.apiUrl}`);
  say(`memory bank: ${connection.bankId}`);
  for (const warning of connection.warnings) {
    warn(warning);
  }
  const environment: Record<string, string> = runtimeEnvironment(connection);

  for (const host of wireable) {
    beginGroup(host.id);
    try {
      const result = await host.install(hostEnvironment, {
        enableWorkspaceMcp: request.enableWorkspaceMcp,
        environment,
      });
      for (const message of result.messages) {
        say(message);
      }
      if (result.ok) {
        say(`wired ${host.displayName}`);
      } else {
        fail(`${host.displayName} was not wired`);
        notInstalled.push(host.id);
      }
    } catch (cause) {
      fail(describeError(cause));
      notInstalled.push(host.id);
    }
  }

  if (request.importConversations) {
    beginGroup("backfill");
    say(await attemptPastSessionBackfill());
  }

  if (notInstalled.length > 0) {
    const names: string[] = selection.hosts
      .map((host: HostContribution) => host.id)
      .filter((id: string) => notInstalled.includes(id));
    fail(`not installed: ${names.join(", ")}`);
    return 1;
  }
  return 0;
}

/**
 * The opt-in backfill. The transcript importer itself belongs to a later slice,
 * so the flag performs the attempt and reports it, and never fails the install.
 */
async function attemptPastSessionBackfill(): Promise<string> {
  return (
    "attempted the past-session backfill; no transcript importer is available in " +
    "this package yet, so nothing was imported"
  );
}
