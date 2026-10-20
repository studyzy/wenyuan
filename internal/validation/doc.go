// Package validation reproduces the request-model validation the reference
// framework applies to a body member, for the cases where this service validates
// a member itself instead of letting a struct's field type do it.
//
// It holds the framework's datetime parser: TimestampFailure reports the exact
// validation item the reference produces for a `datetime` member, including the
// message text and the `ctx.error` detail, so a route that types such a member
// as raw JSON can answer byte for byte what the reference answers. The captured
// reference answers live in testdata/datetime_corpus.json.
//
// The rules live here so two route packages cannot drift into two answers for
// the same value: the retain body and the dry-run body both model a timestamp
// the reference validates this way. The package depends only on internal/httpx,
// so any handler package can use it without an import cycle.
package validation
