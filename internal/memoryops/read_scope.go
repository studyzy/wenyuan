package memoryops

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

const (
	scopeTeam     = "team"
	scopePersonal = "personal"
	scopeBoth     = "both"
)

// memoryReadScopes is the accepted set, as an unknown value is told it. "both"
// is listed because this service recognises it; the item for it says it is not
// available yet.
var memoryReadScopes = []string{scopeTeam, scopePersonal, scopeBoth}

// memoryReadScope reads the `scope` query parameter that chooses which bank a
// bank-scoped read is served from.
//
// The parameter is not part of the reference's contract, so it stays out of the
// request model and out of the OpenAPI document: a route declares it through
// httpx.QueryParams and nothing else knows about it. Absent means "team", which
// is the behaviour the team fallback has always had. An explicit empty value
// (`?scope=`) is the default too, not a typo, because a client templating the
// parameter to "" means "I have no preference".
//
// The two refusals are deliberately different items: "both" is a value this
// service intends to support, so saying it is not supported yet is actionable,
// while an unknown spelling is told the accepted set. Folding the first into
// the second would send a caller looking for a typo that is not there.
//
// Every refusal still returns PreferTeam, so a caller that ignores the failure
// reads the team bank: that is the safe default, not an accidental one.
func memoryReadScope(r *http.Request) (storage.FallbackPreference, *httpx.ValidationFailure) {
	value := httpx.Query(r, "scope")
	switch value {
	case "", scopeTeam:
		return storage.PreferTeam, nil
	case scopePersonal:
		return storage.PreferPersonal, nil
	case scopeBoth:
		return storage.PreferTeam, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.ValueError([]string{"query", "scope"}, value,
				"both is not supported yet; use team or personal"),
		}}
	default:
		return storage.PreferTeam, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.Literal([]string{"query", "scope"}, value, memoryReadScopes),
		}}
	}
}

// MemoryReadScope is memoryReadScope for the other route package that falls
// back: the two reads share one parse so their refusals cannot drift apart.
func MemoryReadScope(r *http.Request) (storage.FallbackPreference, *httpx.ValidationFailure) {
	return memoryReadScope(r)
}
