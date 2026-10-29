#!/usr/bin/env node
/**
 * Integration check: does a wired host actually reach memory?
 *
 * Every other suite in this package stops at "the file says what we wrote". That
 * is the wrong question — the defects this check exists to catch are exactly
 * the ones where the file is right and the host still runs nothing. So this
 * script answers the only question that matters, for real, end to end:
 *
 *   1. stage the runtime into a throwaway home and install a host;
 *   2. read the host's config back the way the host reads it;
 *   3. exec the command string the host would exec, in a real shell;
 *   4. assert the hook launched, and that it talked to the memory service.
 *
 * Step 4 runs against an in-process mock of the service's HTTP API, so the
 * check needs no server, no network and no credentials, and it asserts the
 * request the hook made — route, body, and the document identity the write-back
 * carries — rather than only that "something happened".
 *
 * Usage:
 *   node scripts/integration-check.mjs            # human-readable
 *   node scripts/integration-check.mjs --json     # machine-readable report
 *
 * Exit status is 0 only when every check passes.
 */
import { createServer } from "node:http";
import { execFile } from "node:child_process";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const packageRoot = fileURLToPath(new URL("..", import.meta.url));
const json = process.argv.includes("--json");

/** One check's outcome. */
function check(name, ok, detail) {
  return { name, ok, detail };
}

/**
 * The mock memory service. It records every request and answers the two routes
 * the runtime uses, in the envelopes the real service returns.
 */
async function startMockService() {
  const requests = [];
  const server = createServer((request, response) => {
    let body = "";
    request.on("data", (chunk) => {
      body += chunk.toString();
    });
    request.on("end", () => {
      let parsed = null;
      try {
        parsed = body === "" ? null : JSON.parse(body);
      } catch {
        parsed = { unparseable: body };
      }
      requests.push({
        method: request.method,
        url: request.url,
        body: parsed,
        authorization: request.headers.authorization,
      });
      // The answer is delayed by one tick on purpose. Without it the hook
      // process can exit in the same tick the response is written, and the
      // check reads the record before the request listener has run — which
      // looked exactly like a hook that never called the service.
      const answer = () => {
        response.setHeader("content-type", "application/json");
        if (request.url?.includes("/memories/recall")) {
          response.end(JSON.stringify({ results: [{ id: "m1", text: "the project rounds half-up", type: "observation" }] }));
          return;
        }
        if (request.url?.includes("/memories")) {
          response.end(JSON.stringify({ success: true, items_count: 1, async: false, usage: {} }));
          return;
        }
        response.statusCode = 200;
        response.end("{}");
      };
      setTimeout(answer, 25);
    });
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  return { server, requests, port: address.port };
}

/**
 * Runs one argv to completion in a shell, capturing both streams.
 *
 * `execFile`'s callback fires on exit, not on stdio drain, so a child that
 * printed and exited can still be holding bytes; `close` is awaited to make the
 * captured output complete.
 */
/**
 * The environment one hook runs under.
 *
 * A host launches the hook by absolute path and passes no flag saying which host
 * it is, so the hook resolves that from the harness variable the registration
 * carries. This is the variable the real entry points read, so the check drives
 * them exactly as a host would.
 */
function hookEnv(baseEnv, hostId) {
  return { ...baseEnv, WENYUAN_MCP_HARNESS: hostId };
}

function runCommand(command, { input = "", env } = {}) {
  return new Promise((resolve) => {
    const child = execFile("/bin/sh", ["-c", command], { env, maxBuffer: 8 * 1024 * 1024 });
    let stdout = "";
    let stderr = "";
    let code = null;
    child.stdout?.on("data", (chunk) => {
      stdout += chunk.toString();
    });
    child.stderr?.on("data", (chunk) => {
      stderr += chunk.toString();
    });
    child.on("error", () => {
      code = 1;
    });
    child.on("close", (status) => {
      resolve({ code: code ?? status ?? 0, stdout, stderr });
    });
    child.stdin.end(input);
  });
}

/** The first of the candidate event keys the host actually wrote. */
function pickEvent(events, ...names) {
  return names.map((name) => events[name]).find((value) => value !== undefined);
}

/** The events one host declares, read from the host's own config. */
function eventValues(document) {
  const hooks = document?.hooks;
  return hooks !== null && typeof hooks === "object" && !Array.isArray(hooks) ? hooks : {};
}

/** The command strings a matcher-group host would execute for one event. */
function matcherGroupCommands(value) {
  const groups = Array.isArray(value) ? value : [value];
  return groups
    .filter((group) => group !== null && typeof group === "object" && Array.isArray(group.hooks))
    .flatMap((group) => group.hooks)
    .filter((entry) => entry !== null && typeof entry === "object" && typeof entry.command === "string")
    .map((entry) => entry.command);
}

/** The command string a flat-entry host would execute for one event. */
function flatCommands(value) {
  return value !== null && typeof value === "object" && typeof value.command === "string" ? [value.command] : [];
}

/** The hosts this check drives, and how to read each one's registration. */
const HOSTS = [
  { id: "claude-code", config: ".claude/settings.json", commands: matcherGroupCommands },
  { id: "codex", config: ".codex/hooks.json", commands: matcherGroupCommands },
  { id: "codebuddy", config: ".codebuddy/settings.json", commands: matcherGroupCommands },
  { id: "cursor-cli", config: ".cursor/hooks.json", commands: flatCommands },
  { id: "copilot-cli", config: ".copilot/hooks/hooks.json", commands: flatCommands },
];

/** The prompt payload a host hands its prompt hook, in the hosts' own fields. */
function promptPayload(sessionId, prompt) {
  return JSON.stringify({ session_id: sessionId, prompt, hook_event_name: "UserPromptSubmit", cwd: process.cwd() });
}

/**
 * A Claude Code transcript: JSONL, one record per event, with the role on
 * `type` and the payload under `message` — and a non-message record mixed in,
 * which the reader must skip rather than mis-read.
 */
function claudeTranscript(turns) {
  const records = [{ type: "summary", summary: "a session" }];
  for (const [role, text] of turns) {
    records.push({
      type: role,
      message: { role, content: [{ type: "text", text }] },
      uuid: `${role}-${text.length}`,
      timestamp: new Date(0).toISOString(),
    });
  }
  return `${records.map((record) => JSON.stringify(record)).join("\n")}\n`;
}

/**
 * Runs the whole check. Returns the report; never throws for a failed check.
 */
async function main() {
  const results = [];
  const service = await startMockService();
  const base = `http://127.0.0.1:${service.port}`;
  const home = await mkdtemp(join(tmpdir(), "wenyuan-integration-"));
  if (process.env.INTEGRATION_DEBUG === "1") process.stderr.write(`home: ${home}\n`);
  const baseEnv = {
    ...process.env,
    HOME: home,
    WENYUAN_CONFIG: join(home, ".wenyuan", "config.json"),
    PATH: process.env.PATH ?? "/usr/bin:/bin",
  };
  const env = baseEnv;
  // No host CLI on PATH: the installer reports the manual MCP line instead of
  // failing the run, and this check is about hooks reaching memory.
  try {
    // The hosts are named explicitly rather than detected: with no host CLI on
    // PATH and no host config directory yet, detection would find none — and
    // naming them is also the path a user takes on a machine being set up.
    const install = await runCommand(
      `node "${join(packageRoot, "dist", "cli", "main.mjs")}" install ${HOSTS.map((host) => host.id).join(" ")} ` +
        `--server self-hosted --api-url ${base} --api-token integration-check`,
      { env },
    );
    // Claude Code's MCP entry can only be written through its own CLI, which is
    // deliberately absent here — that is the case the manual line exists for, so
    // exit 1 with Claude named is the expected outcome of this run, not a
    // failure. Every file the installer owns must still have been written.
    const namedClaude = /Claude Code was not wired/.test(`${install.stdout}${install.stderr}`);
    results.push(
      check(
        "install every host",
        install.code === 0 || (install.code === 1 && namedClaude),
        install.code === 0 ? "exited 0" : `exited ${install.code} (reported the manual MCP step)`,
      ),
    );

    for (const host of HOSTS) {
      const configPath = join(home, host.config);
      let document;
      try {
        document = JSON.parse(await readFile(configPath, "utf8"));
      } catch (error) {
        results.push(check(`${host.id}: config exists`, false, `${configPath}: ${error.message}`));
        continue;
      }
      const events = eventValues(document);
      const sessionId = `check-${host.id}`;

      const sessionStart = host.commands(pickEvent(events, "SessionStart", "sessionStart"));
      const prompt = host.commands(pickEvent(events, "UserPromptSubmit", "beforeSubmitPrompt", "userPromptTransformed"));
      // `stop` is also `Array.prototype.stop`-free but a plain property lookup on
      // an object is what this is: the host's own event key, spelled per host.
      const stop = host.commands(pickEvent(events, "Stop", "stop", "agentStop"));

      results.push(
        check(
          `${host.id}: three events registered`,
          sessionStart.length > 0 && prompt.length > 0 && stop.length > 0,
          `${Object.keys(events).join(", ") || "no events"}`,
        ),
      );

      // The prompt hook: a real shell exec of the exact command the host runs.
      if (prompt.length > 0) {
        const before = service.requests.length;
        const run = await runCommand(prompt[0], { env: hookEnv(baseEnv, host.id), input: promptPayload(sessionId, "how do we round money?") });
        const recall = service.requests.slice(before).find((entry) => entry.url?.includes("/memories/recall"));
        results.push(check(`${host.id}: prompt hook exits 0`, run.code === 0, `exit ${run.code}${run.stderr ? `: ${run.stderr.trim()}` : ""}`));
        results.push(
          check(
            `${host.id}: prompt hook calls recall`,
            recall !== undefined && recall.body?.query === "how do we round money?",
            recall === undefined ? "no recall request" : `POST ${recall.url}`,
          ),
        );
        const injected = run.stdout.includes("the project rounds half-up");
        results.push(check(`${host.id}: recalled text reaches the host`, injected, run.stdout.trim().slice(0, 160) || "no stdout"));
      }

      // The session-start hook.
      if (sessionStart.length > 0) {
        const before = service.requests.length;
        const run = await runCommand(sessionStart[0], { env: hookEnv(baseEnv, host.id), input: JSON.stringify({ session_id: sessionId, hook_event_name: "SessionStart" }) });
        const recall = service.requests.slice(before).find((entry) => entry.url?.includes("/memories/recall"));
        results.push(check(`${host.id}: session-start hook calls recall`, run.code === 0 && recall !== undefined, run.code === 0 ? `POST ${recall?.url ?? "none"}` : `exit ${run.code}`));
      }

      // The stop hook, with the transcript a real Stop payload points at. The
      // payload itself carries no turns — that is the whole point — so what is
      // asserted here is that the hook reads the transcript and retains it.
      if (stop.length > 0) {
        const transcriptPath = join(home, `${sessionId}.jsonl`);
        await writeFile(
          transcriptPath,
          claudeTranscript([
            ["user", "why do we round half-up here"],
            ["assistant", "because the ledger reconciles to cents"],
          ]),
        );
        const before = service.requests.length;
        // The host's registered event, folded into the environment the way the
        // packaged entry point reads it (a hook is launched with no payload
        // flag saying which event it serves).
        const run = await runCommand(stop[0], {
          env: { ...hookEnv(baseEnv, host.id), WENYUAN_HOOK_EVENT: "Stop" },
          input: JSON.stringify({
            session_id: sessionId,
            transcript_path: transcriptPath,
            hook_event_name: "Stop",
            stop_hook_active: false,
            cwd: process.cwd(),
          }),
        });
        const write = service.requests
          .slice(before)
          .find((entry) => entry.method === "POST" && entry.url?.endsWith("/memories"));
        const item = write?.body?.items?.[0];
        results.push(
          check(
            `${host.id}: stop hook exits 0`,
            run.code === 0,
            `exit ${run.code}${run.stderr ? `: ${run.stderr.trim().slice(0, 200)}` : ""}; requests=${service.requests.length - before}; ${JSON.stringify(service.requests.slice(before))}`.slice(0, 400),
          ),
        );
        results.push(
          check(
            `${host.id}: stop hook reads the transcript and retains it`,
            item !== undefined &&
              typeof item.content === "string" &&
              item.content.includes("round half-up") &&
              item.content.includes("reconciles to cents"),
            item === undefined ? "no write request" : item.content.slice(0, 120),
          ),
        );
        results.push(
          check(
            `${host.id}: write-back carries one document id`,
            item?.document_id === `conversation:${sessionId}` && item?.update_mode !== undefined,
            item === undefined ? "no write request" : `document_id=${item.document_id} update_mode=${item.update_mode}`,
          ),
        );

        // A second stop on the same session must append, not resend: the
        // transcript grew by one turn, so the write carries that turn only.
        await writeFile(
          transcriptPath,
          claudeTranscript([
            ["user", "why do we round half-up here"],
            ["assistant", "because the ledger reconciles to cents"],
            ["user", "and the retry policy?"],
          ]),
        );
        const beforeSecond = service.requests.length;
        await runCommand(stop[0], {
          env: hookEnv(baseEnv, host.id),
          input: JSON.stringify({ session_id: sessionId, transcript_path: transcriptPath, hook_event_name: "Stop" }),
        });
        const second = service.requests
          .slice(beforeSecond)
          .find((entry) => entry.method === "POST" && entry.url?.endsWith("/memories"));
        const secondItem = second?.body?.items?.[0];
        results.push(
          check(
            `${host.id}: a second stop appends only the new turn`,
            secondItem?.update_mode === "append" &&
              typeof secondItem?.content === "string" &&
              secondItem.content.includes("retry policy") &&
              !secondItem.content.includes("round half-up"),
            secondItem === undefined ? "no second write" : `update_mode=${secondItem.update_mode} content=${JSON.stringify(secondItem.content)}`,
          ),
        );
      }
    }

    // The MCP server: the handshake and the tool surface a host would see,
    // driven over a real stdio pipe.
    const mcp = await runCommand(`node "${join(home, ".wenyuan", "wenyuan-memory", "mcp-server.mjs")}"`, {
      env,
      input: `${JSON.stringify({ jsonrpc: "2.0", id: 1, method: "initialize", params: { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name: "check", version: "1" } } })}\n${JSON.stringify({ jsonrpc: "2.0", id: 2, method: "tools/list" })}\n`,
    });
    const replies = mcp.stdout
      .split("\n")
      .filter((line) => line.trim() !== "")
      .map((line) => JSON.parse(line));
    const initialize = replies.find((reply) => reply.id === 1);
    const tools = replies.find((reply) => reply.id === 2);
    results.push(check("mcp: initialize answers", initialize?.result?.protocolVersion === "2025-06-18", JSON.stringify(initialize?.result?.serverInfo ?? null)));
    results.push(
      check(
        "mcp: tools listed",
        Array.isArray(tools?.result?.tools) && tools.result.tools.length > 0,
        `${tools?.result?.tools?.length ?? 0} tool(s): ${(tools?.result?.tools ?? []).map((tool) => tool.name).join(", ")}`,
      ),
    );

    // Every request the hooks made carried the bearer token the install stored.
    const tokens = service.requests.map((entry) => entry.authorization).filter((value) => value !== undefined);
    results.push(check("hooks authenticate", tokens.length === service.requests.length, `${tokens.length}/${service.requests.length} requests carried a token`));
  } finally {
    if (process.env.INTEGRATION_DEBUG === "1") {
      // A debugging run leaves the home directory in place so the wired config
      // and the hook state can be inspected after the fact.
      process.stderr.write(`home kept: ${home}\n`);
      process.stderr.write(`requests seen: ${JSON.stringify(service.requests.map((entry) => `${entry.method} ${entry.url}`))}\n`);
    } else {
      await rm(home, { recursive: true, force: true });
    }
    service.server.close();
  }

  const failed = results.filter((result) => !result.ok);
  if (json) {
    process.stdout.write(`${JSON.stringify({ ok: failed.length === 0, results }, null, 2)}\n`);
  } else {
    for (const result of results) {
      process.stdout.write(`${result.ok ? "PASS" : "FAIL"}  ${result.name}${result.ok ? "" : `  — ${result.detail}`}\n`);
    }
    process.stdout.write(`\n${results.length - failed.length}/${results.length} checks passed\n`);
  }
  return failed.length === 0 ? 0 : 1;
}

process.exit(await main());