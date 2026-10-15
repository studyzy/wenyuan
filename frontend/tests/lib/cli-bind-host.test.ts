import { describe, it } from "vitest";

/**
 * SKIPPED: these cases load `bin/cli.js`, the launcher that ships with the
 * control-plane package, and test its `resolveBindHost` helper. That launcher is
 * not part of this frontend (we ship no `bin/`), and the helper has no
 * counterpart here. Kept as skipped placeholders so the gap stays visible
 * instead of being silently dropped.
 */
describe.skip("cli resolveBindHost (#1926) (bin/cli.js not shipped)", () => {
  it("binds a loopback literal as localhost, keeping the requested family", () => {});
  it("appends to an operator's NODE_OPTIONS instead of replacing it", () => {});
  it("leaves every other hostname and NODE_OPTIONS untouched", () => {});
});