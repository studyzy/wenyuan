package httpx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
)

type ignoredContextKey struct{}

// QueryFilter declares the query parameters a route accepts. Unknown
// parameters are never rejected; they are collected and reported through the
// ignored-parameters header.
type QueryFilter struct {
	Allowed map[string]struct{}
}

// NewQueryFilter builds a filter from an allowlist of accepted parameter names.
func NewQueryFilter(names ...string) QueryFilter {
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		allowed[name] = struct{}{}
	}
	return QueryFilter{Allowed: allowed}
}

// Unknown returns the unknown parameter names present on the request, sorted
// for deterministic output.
func (f QueryFilter) Unknown(r *http.Request) []string {
	query := r.URL.Query()
	unknown := make([]string, 0)
	for name := range query {
		if _, ok := f.Allowed[name]; ok {
			continue
		}
		unknown = append(unknown, name)
	}
	sort.Strings(unknown)
	return unknown
}

// RecordIgnoredParams annotates the request context with unknown parameter
// names so the reporting middleware can emit the header after the handler runs.
func RecordIgnoredParams(r *http.Request, names []string) *http.Request {
	if len(names) == 0 {
		return r
	}
	ctx := context.WithValue(r.Context(), ignoredContextKey{}, names)
	return r.WithContext(ctx)
}

func ignoredParamsFrom(ctx context.Context) []string {
	names, _ := ctx.Value(ignoredContextKey{}).([]string)
	return names
}

// RecordIgnoredParamsFor is the convenience path used by handlers: it derives
// the unknown parameters from the filter and records them on the request.
func RecordIgnoredParamsFor(r *http.Request, filter QueryFilter) *http.Request {
	return RecordIgnoredParams(r, filter.Unknown(r))
}

// UnknownBodyFields returns the top-level object keys of a JSON body that are
// not in the allowed set, sorted for deterministic output. A body that is not
// a JSON object yields nothing, because the route's own decoder reports that
// failure. This is the body counterpart of QueryFilter.Unknown: an unknown body
// field is never rejected, only reported.
func UnknownBodyFields(body []byte, allowed map[string]struct{}) []string {
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return nil
	}
	unknown := make([]string, 0)
	for name := range raw {
		if _, ok := allowed[name]; ok {
			continue
		}
		unknown = append(unknown, name)
	}
	sort.Strings(unknown)
	return unknown
}

// RecordIgnoredBodyFields annotates the request context with unknown body field
// names, merging them with any query parameters already recorded so the header
// reports both.
func RecordIgnoredBodyFields(r *http.Request, allowed map[string]struct{}) ([]byte, *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, r
	}
	_ = r.Body.Close()
	names := UnknownBodyFields(body, allowed)
	if len(names) == 0 {
		return body, r
	}
	merged := append(ignoredParamsFrom(r.Context()), names...)
	return body, RecordIgnoredParams(r, merged)
}

func formatIgnoredParams(names []string) string {
	return strings.Join(names, ",")
}
