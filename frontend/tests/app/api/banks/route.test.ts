import { describe, it } from "vitest";

/**
 * SKIPPED: this case drives `GET /api/banks` by mocking `@/lib/hindsight-client`
 * — the reference frontend's generated dataplane SDK. We deliberately do not ship
 * that SDK: our route handlers reach the service through `lib/server/proxy` and
 * `lib/server/http` instead, so every assertion here pins the SDK's call shape
 * (`sdk.listBanks({ query: … })`), not HTTP behaviour. Contorting our plumbing to
 * satisfy it would test a seam we do not have; our equivalent coverage lives in
 * the `test/server-*` suite. Kept as skipped placeholders so the gap stays
 * visible instead of being silently dropped.
 */
describe.skip("GET /api/banks (mocks @/lib/hindsight-client, not shipped)", () => {
  it("forwards the search term and the paging window to the dataplane", () => {});
  it("sends offset=0 rather than dropping it (the first page is explicit)", () => {});
  it("omits every param when none are provided, so the dataplane defaults apply", () => {});
});