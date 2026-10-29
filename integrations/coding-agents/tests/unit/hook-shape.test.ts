import { describe, expect, it } from "vitest";
import {
  HOOKS_CONTAINER_KEY,
  HOOK_COMMAND_TYPE,
  MATCHER_GROUP_KEY,
  commandEntriesOf,
  encodeRegistration,
  registrationIsOwned,
  type HookCommandEntry,
} from "../../src/hosts/hook-shape";
import { OWNERSHIP_MARKER } from "../../src/write/marker";
import type { LifecycleRegistration, RegistrationStyle } from "../../src/hosts/lifecycle-types";

/**
 * The shape encoder turns one declared registration into the host's own nested
 * structure and decodes it back, so that the installer, a re-install and the
 * removal all agree on what is this package's own entry.
 */

const PATH = `/home/u/.wenyuan/${OWNERSHIP_MARKER}/hooks/session-start.mjs`;

function registration(style: RegistrationStyle): LifecycleRegistration {
  return { event: "SessionStart", scriptFile: "session-start.mjs", budget: 10, budgetUnit: "seconds", style };
}

describe("encoding one registration", () => {
  it("writes the matcher-group shape the first-batch hosts read", () => {
    const value = encodeRegistration(registration("matcher-group"), PATH, 10);
    expect(value).toEqual({
      [MATCHER_GROUP_KEY]: [{ type: HOOK_COMMAND_TYPE, command: PATH, timeout: 10 }],
    });
  });

  it("puts exactly one command entry inside the matcher group", () => {
    const value = encodeRegistration(registration("matcher-group"), PATH, 10) as unknown as Record<string, unknown>;
    expect(value[MATCHER_GROUP_KEY]).toHaveLength(1);
  });

  it("carries the execution type, the script path and the numeric budget on the entry", () => {
    const entries: HookCommandEntry[] = commandEntriesOf(encodeRegistration(registration("matcher-group"), PATH, 42));
    expect(entries).toEqual([{ type: HOOK_COMMAND_TYPE, command: PATH, timeout: 42 }]);
  });

  it("encodes the declared alternative styles distinctly", () => {
    const flat = encodeRegistration(registration("flat-entry"), PATH, 10);
    expect(Array.isArray(flat)).toBe(false);
    expect(commandEntriesOf(flat)).toHaveLength(1);

    const table = encodeRegistration(registration("table-array"), PATH, 10);
    expect(Array.isArray(table)).toBe(true);
    expect(commandEntriesOf(table)).toHaveLength(1);
  });

  it("writes one flat command entry for a host that reads no matcher group", () => {
    expect(encodeRegistration(registration("flat-entry"), PATH, 10)).toEqual({
      type: HOOK_COMMAND_TYPE,
      command: PATH,
      timeout: 10,
    });
  });
});

describe("decoding an event value", () => {
  it("reads command entries out of a matcher group, an array and a single entry alike", () => {
    const group = { [MATCHER_GROUP_KEY]: [{ type: HOOK_COMMAND_TYPE, command: PATH, timeout: 10 }] };
    const table = [{ type: HOOK_COMMAND_TYPE, command: PATH, timeout: 10 }];
    const single = { type: HOOK_COMMAND_TYPE, command: PATH, timeout: 10 };
    for (const value of [group, table, single]) {
      expect(commandEntriesOf(value)).toEqual([{ type: HOOK_COMMAND_TYPE, command: PATH, timeout: 10 }]);
    }
  });

  it("decodes an array element by element, finding a nested matcher group", () => {
    const own = { [MATCHER_GROUP_KEY]: [{ type: HOOK_COMMAND_TYPE, command: PATH, timeout: 10 }] };
    const list = [{ command: "echo foreign" }, own];
    expect(commandEntriesOf(list)).toEqual([{ type: HOOK_COMMAND_TYPE, command: PATH, timeout: 10 }]);
  });

  it("treats a foreign or malformed value as holding no command entries", () => {
    expect(commandEntriesOf(undefined)).toEqual([]);
    expect(commandEntriesOf(null)).toEqual([]);
    expect(commandEntriesOf("echo hi")).toEqual([]);
    expect(commandEntriesOf({ command: "no-timeout" })).toEqual([]);
    expect(commandEntriesOf({ [MATCHER_GROUP_KEY]: [{ type: HOOK_COMMAND_TYPE, command: "x" }] })).toEqual([]);
  });
});

describe("recognizing this package's own entry", () => {
  it("recognizes an entry whose command carries the ownership marker", () => {
    expect(registrationIsOwned(encodeRegistration(registration("matcher-group"), PATH, 10))).toBe(true);
  });

  it("does not recognize a foreign entry on the same event", () => {
    expect(registrationIsOwned({ [MATCHER_GROUP_KEY]: [{ type: HOOK_COMMAND_TYPE, command: "echo foreign", timeout: 10 }] })).toBe(false);
  });

  it("does not recognize an empty or malformed value", () => {
    expect(registrationIsOwned(undefined)).toBe(false);
    expect(registrationIsOwned({})).toBe(false);
  });
});

describe("the shared key spellings", () => {
  it("keeps the container, command-type and matcher-group keys as the contract spells them", () => {
    expect(HOOKS_CONTAINER_KEY).toBe("hooks");
    expect(HOOK_COMMAND_TYPE).toBe("command");
    expect(MATCHER_GROUP_KEY).toBe("matchers");
  });
});