import { describe, expect, it } from "vitest";
import { hookPathFor } from "../../src/hosts/hooks";
import { HOOKS_SUBDIRECTORY } from "../../src/runtime/layout";
import { OWNERSHIP_MARKER } from "../../src/write/marker";

/**
 * The command path a host config carries.
 *
 * It is always the absolute path into the staged runtime and always carries the
 * ownership marker, because that marker is the only thing install, re-install
 * and uninstall use to recognize their own entries.
 */

const RUNTIME = `/home/u/.wenyuan/${OWNERSHIP_MARKER}`;
const SCRIPT = "session-start.mjs";

describe("the staged hook path", () => {
  it("points into the hooks subdirectory of the staged runtime", () => {
    expect(hookPathFor(SCRIPT, RUNTIME)).toBe(`${RUNTIME}/${HOOKS_SUBDIRECTORY}/${SCRIPT}`);
  });

  it("carries the ownership marker, so removal still finds it", () => {
    expect(hookPathFor(SCRIPT, RUNTIME)).toContain(OWNERSHIP_MARKER);
  });

  it("is absolute, so a host need not resolve anything at launch", () => {
    expect(hookPathFor(SCRIPT, RUNTIME).startsWith("/")).toBe(true);
  });
});
