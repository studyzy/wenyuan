import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { resolveHostConfig, resolveHostMemory } from "../../../src/core/host-client";

let root: string;
let cfgPath: string;

/** The config file location is resolved per call, so each case points WENYUAN_AGENTS_CONFIG at
 *  its own file and no module re-import dance is needed. */
function writeConfig(value: unknown): void {
  mkdirSync(join(cfgPath, ".."), { recursive: true });
  process.env.WENYUAN_AGENTS_CONFIG = cfgPath;
  writeCfgFile(cfgPath, value);
}

function writeCfgFile(path: string, value: unknown): void {
  writeFileSync(path, JSON.stringify(value));
}

const ENV = { ...process.env };

beforeEach(() => {
  root = mkdtempSync(join(tmpdir(), "wenyuan-host-"));
  cfgPath = join(root, "config.json");
});

afterEach(() => {
  process.env = { ...ENV };
  rmSync(root, { recursive: true, force: true });
});

describe("resolveHostMemory", () => {
  it("forwards every client setting the hosts used to pass by hand", () => {
    // Hosts that hand-build their client each forget a different field (one forgot the parallel
    // cap, another the opt-in gate). One builder is what stops that recurring.
    writeConfig({ apiUrl: "http://server", apiToken: "k", maxParallelRetains: 3 });

    const { client } = resolveHostMemory("dsh", root);
    expect(client.apiUrl).toBe("http://server");
    expect(client.apiToken).toBe("k");
    expect(client.maxParallelRetains).toBe(3);
  });

  // The host client must name no project on a shared bank: an initiative captured in a repo-owned
  // bank is scoped to that repo, while a shared bank gets the shared-bank clause.
  it("names the repo as the page subject only when the bank is that repo's", () => {
    const repo = join(root, "my-repo");
    mkdirSync(repo);
    execFileSync("git", ["init", "-q", repo]);
    writeConfig({});
    const own = resolveHostMemory("dsh", repo);
    expect(own.client.project).toBe("my-repo");
    expect(own.bankId).toContain("my-repo");

    writeConfig({ bankId: "shared-memory" });
    expect(resolveHostMemory("dsh", repo).client.project).toBeUndefined();
  });

  it("enforces optInOnly for every host, not just the ones that remembered to pass a directory", () => {
    // A host calling applyBankConfig WITHOUT the directory never enforced `optInOnly`: an
    // unapproved repo still got a bank and still had memories written for it.
    writeConfig({ optInOnly: true, optInPaths: ["/somewhere/else"] });

    expect(resolveHostMemory("dsh", root).cfg.disabled).toBe(true);
  });

  it("does not derive a bank when memory is disabled — that path is a zero-overhead baseline", () => {
    // Bank derivation probes the repository layout. `disabled` promises the same agent with NO
    // memory work, which is what makes it usable as an A/B baseline, so it must stop before that.
    writeConfig({ disabled: true });

    const { cfg, bankId } = resolveHostMemory("dsh", root);
    expect(cfg.disabled).toBe(true);
    expect(bankId).toBe("");
  });

  it("honours a per-bank apiToken, not just the top-level one", () => {
    // `banks.<id>.apiToken` is a legitimate override (it is not stripped by the override
    // sanitizer), so a host that read only the top level would sign with the wrong credential.
    writeConfig({ apiUrl: "http://server", apiToken: "global" });
    const bankId = resolveHostMemory("dsh", root).bankId;

    writeConfig({
      apiUrl: "http://server",
      apiToken: "global",
      banks: { [bankId]: { apiToken: "per-bank" } },
    });
    expect(resolveHostMemory("dsh", root).client.apiToken).toBe("per-bank");
  });

  it("exposes the same workspace view through resolveHostConfig", () => {
    writeConfig({ apiUrl: "http://server", apiToken: "k" });
    const { cfg, bankId, project } = resolveHostConfig("dsh", root);
    expect(cfg.apiToken).toBe("k");
    expect(bankId).toContain("coding-agent::");
    // The name is whatever {gitProject} resolved for the same directory — on this machine the
    // temp tree can sit inside a repository, so assert agreement with the bank id, not a literal.
    expect(project).toBeTruthy();
    expect(bankId.endsWith(`::${project}`)).toBe(true);
  });
});

/**
 * A capability wired per-host is a capability the next host forgets. Settings had already gone
 * missing that way (the parallel cap twice, the opt-in gate once, the live credential
 * everywhere), so the rule is structural — a long-lived host does not build its own client, and a
 * module that does has to say why.
 */
describe("long-lived hosts build their client through the shared factory", () => {
  const SRC = fileURLToPath(new URL("../../../src", import.meta.url));

  /** Modules that construct a client directly, and why that is correct for them. */
  const DIRECT: Record<string, string> = {
    "core/host-client.ts": "the shared factory itself",
    // The pre-core hook runtime: it builds its client from the installer-persisted connection,
    // not from a workspace config, and is replaced by the core stack wholesale in a later wave.
    "core/hook.ts": "the hook runtime itself, built from the resolved session config",
    "core/retain-hook.ts": "the stop-path runtime, built from the resolved session config",
    "core/session-start.ts": "the session-start runtime, built from the resolved config",
    "core/deepen.ts": "the background engine, built from the deepen lease config",
    "runtime/mcp-server.ts": "the stdio MCP server serves the one bank the stored installer connection names",
  };

  function sourceFiles(dir: string, prefix = ""): string[] {
    return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
      const rel = prefix ? `${prefix}/${entry.name}` : entry.name;
      if (entry.isDirectory()) return sourceFiles(join(dir, entry.name), rel);
      return entry.name.endsWith(".ts") && !entry.name.includes(".test.") ? [rel] : [];
    });
  }

  it("has no module constructing a client outside the factory without a stated reason", () => {
    const unexplained = sourceFiles(SRC).filter(
      (rel) =>
        !(rel in DIRECT) && readFileSync(join(SRC, rel), "utf8").includes("new MemoryClient(")
    );
    expect(unexplained).toEqual([]);
  });

  it("keeps no entry for a module that stopped constructing one", () => {
    const stale = Object.keys(DIRECT).filter(
      (rel) => !readFileSync(join(SRC, rel), "utf8").includes("new MemoryClient(")
    );
    expect(stale).toEqual([]);
  });
});
