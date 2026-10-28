import { describe, it } from "vitest";

/**
 * SKIPPED: this case drives `POST /api/recall` by mocking the reference's generated client package
 * (`sdk.recallMemories`) — the reference frontend's generated dataplane SDK, which
 * we deliberately do not ship in favour of `lib/server/proxy`. The assertion pins
 * the SDK call's argument shape, not our HTTP behaviour, so it cannot be carried
 * over without contorting our data-access layer. See the `test/server-*` suite for
 * our equivalent coverage. Kept skipped so the gap stays visible.
 */
describe.skip("POST /api/recall (mocks the reference's generated client package, not shipped)", () => {
  it("returns source_facts and source_facts_truncated from the dataplane", () => {});
});