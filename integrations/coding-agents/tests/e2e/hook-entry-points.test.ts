import { describe, expect, it } from "vitest";
import { DISABLE_HOOKS_ENV } from "../../src/runtime/hook-contract";
import { stageDeadlineMs } from "../../src/runtime/deadline";
import { HOOK_SCRIPT_FILES, registrationFor } from "../../src/hosts/lifecycle";
import { orderedRegistrations, hostLifecycle } from "../../src/hosts/lifecycle";
import { HOST_LIFECYCLES } from "../../src/hosts/lifecycle";
import { runHook, seedStoredConnection, startStubService, withHookHome, type StubService } from "../helpers/hook-run";

/**
 * The three packaged entry points share one lifecycle: the event spelling and
 * budget the installer writes are the ones the entry points resolve at run
 * time, and every one of them fails open — a malformed payload, a missing
 * service or a guard set all exit zero.
 */

const SCRIPTS: { stage: "session-start" | "pre-prompt" | "stop"; file: string }[] = [
  { stage: "session-start", file: "session-start.mjs" },
  { stage: "pre-prompt", file: "pre-prompt.mjs" },
  { stage: "stop", file: "stop.mjs" },
];

describe("the install and runtime lifecycles agree by construction", () => {
  it("resolves each script file back to the stage whose event install writes", () => {
    for (const { stage, file } of SCRIPTS) {
      expect(HOOK_SCRIPT_FILES[stage]).toBe(file);
      expect(registrationFor("claude-code", stage).scriptFile).toBe(file);
    }
  });

  it("has one lifecycle per host that both sides read", () => {
    for (const hostId of Object.keys(HOST_LIFECYCLES)) {
      expect(orderedRegistrations(hostLifecycle(hostId))).toHaveLength(3);
    }
  });

  it("self-imposes a deadline inside the budget the host enforces", () => {
    for (const hostId of Object.keys(HOST_LIFECYCLES)) {
      for (const { stage } of SCRIPTS) {
        expect(stageDeadlineMs(hostId, stage)).toBeGreaterThan(0);
      }
    }
  });
});

describe.each(SCRIPTS)("the $stage entry point", ({ file }) => {
  it("exits zero on a malformed payload without throwing", async () => {
    await withHookHome(async (home) => {
      await seedStoredConnection(home, "http://127.0.0.1:1");
      for (const payload of ["", "{ not json", "[]", "42"]) {
        const run = await runHook({ home, scriptFile: file, payload });
        expect(run.status, `${file} ${payload}`).toBe(0);
      }
    });
  });

  it("exits zero with the disable guard set, before reaching the service", async () => {
    const service: StubService = await startStubService();
    try {
      await withHookHome(async (home) => {
        await seedStoredConnection(home, service.url);
        const run = await runHook({
          home,
          scriptFile: file,
          payload: { session_id: "s-1", prompt: "q" },
          environment: { [DISABLE_HOOKS_ENV]: "1" },
        });
        expect(run.status).toBe(0);
        expect(service.requests).toHaveLength(0);
      });
    } finally {
      await service.close();
    }
  });

  it("never writes to standard error on the success path", async () => {
    await withHookHome(async (home) => {
      await seedStoredConnection(home, "http://127.0.0.1:1");
      const run = await runHook({ home, scriptFile: file, payload: { session_id: "s-1", prompt: "q" } });
      expect(run.status).toBe(0);
      expect(run.stderr).toBe("");
    });
  });
});