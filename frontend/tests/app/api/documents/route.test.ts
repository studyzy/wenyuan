import { describe, it } from "vitest";

/**
 * SKIPPED: this case drives `GET`/`PATCH`/`DELETE /api/documents` by mocking
 * `@/lib/hindsight-client` (`sdk.listDocuments`/`getDocument`/`deleteDocument`)
 * and its URL helpers — the reference frontend's generated dataplane SDK, which we
 * deliberately do not ship. Every assertion pins the SDK call's argument shape or
 * a URL the SDK builds, not our HTTP behaviour, so none can be carried over
 * without contorting our `lib/server/proxy` data-access layer. The behaviours they
 * guard (query-param `q`/`tags`/`tags_match` forwarding, a slash-bearing
 * `document_id` surviving intact) are ours too, covered by `test/server-*`. Kept
 * skipped so the gap stays visible.
 */
describe.skip("GET /api/documents (mocks @/lib/hindsight-client, not shipped)", () => {
  it("forwards the `q` search term to the dataplane (search by document ID)", () => {});
  it("omits `q` when no search term is provided", () => {});
  it("forwards repeated `tags` params and `tags_match` to the dataplane", () => {});
  it("omits `tags` when none are provided", () => {});
  it("drops `tags_match` when no tags are filtered, so the dataplane default stands", () => {});
  it("rejects an unknown `tags_match` value rather than passing it through", () => {});
  it("fetches a single document when `document_id` is present, slashes intact", () => {});
});

describe.skip("single-document operations on /api/documents (mocks @/lib/hindsight-client, not shipped)", () => {
  it("DELETE passes the id through to the dataplane intact", () => {});
  it("PATCH re-encodes the id for the dataplane path", () => {});
  it("rejects a PATCH with no document_id", () => {});
});