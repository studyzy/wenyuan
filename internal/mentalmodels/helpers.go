package mentalmodels

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// maxMentalModelTokens is the reference's max_tokens bound on the create and
// update bodies (ge=256, le=8192), with 2048 the create default.
const (
	minMentalModelTokens     = 256
	maxMentalModelTokens     = 8192
	defaultMentalModelTokens = 2048
)

// detailValues and tagsMatchValues are the reference's Literals for the list and
// get routes; a value outside them is a 422 literal_error.
var (
	detailValues    = []string{"metadata", "content", "full"}
	tagsMatchValues = []string{"any", "all", "exact"}
)

// detailParam validates the detail selector. The reference declares it as a
// Literal, so an out-of-enum value is a 422 even though the field is optional;
// only absence falls back to the default.
func detailParam(w http.ResponseWriter, r *http.Request, fallback string) (string, bool) {
	raw := strings.TrimSpace(httpx.Query(r, "detail"))
	if raw == "" {
		return fallback, true
	}
	if !contains(detailValues, raw) {
		writeQueryItem(w, httpx.Literal([]string{"query", "detail"}, raw, detailValues))
		return "", false
	}
	return raw, true
}

// tagsMatchParam validates the tags_match selector against the reference's
// Literal, rejecting an unknown mode rather than silently treating it as any.
func tagsMatchParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw := strings.TrimSpace(httpx.Query(r, "tags_match"))
	if raw == "" {
		return "any", true
	}
	if !contains(tagsMatchValues, raw) {
		writeQueryItem(w, httpx.Literal([]string{"query", "tags_match"}, raw, tagsMatchValues))
		return "", false
	}
	return raw, true
}

// pageParams reads limit and offset with the reference's bounds: limit is
// 1..1000 defaulting to 100, offset is >= 0 defaulting to 0. A malformed or
// out-of-range value writes the 422 envelope and reports false.
func pageParams(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	limit, ok := boundedQueryInt(w, r, "limit", 100, 1, 1000)
	if !ok {
		return 0, 0, false
	}
	offset, ok := boundedQueryInt(w, r, "offset", 0, 0, 0)
	if !ok {
		return 0, 0, false
	}
	return limit, offset, true
}

// boundedQueryInt reads an optional integer query parameter. Absence yields def;
// a malformed value is int_parsing and an out-of-range one names the bound. A
// max below min means no upper bound.
func boundedQueryInt(w http.ResponseWriter, r *http.Request, name string, def, min, max int) (int, bool) {
	raw := strings.TrimSpace(httpx.Query(r, name))
	if raw == "" {
		return def, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		writeQueryItem(w, httpx.TypeMismatch("int_parsing", []string{"query", name}, raw))
		return 0, false
	}
	// The framework reports the raw query string as the item's input, not the
	// parsed integer.
	if value < min {
		writeQueryItem(w, httpx.GreaterThanEqual([]string{"query", name}, raw, min))
		return 0, false
	}
	if max >= min && value > max {
		writeQueryItem(w, httpx.LessThanEqual([]string{"query", name}, raw, max))
		return 0, false
	}
	return value, true
}

// writeQueryItem emits the framework's structured validation body for one
// rejected query parameter.
func writeQueryItem(w http.ResponseWriter, item httpx.ValidationError) {
	httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{item}})
}

// pageSlice applies offset and limit to a matched list, tolerating an offset
// past the end.
func pageSlice[T any](items []T, offset, limit int) []T {
	if offset >= len(items) {
		return []T{}
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}

// matchesTags reports whether the record's tags satisfy the requested set. The
// default mode is any; "all"/"all_strict" require every requested tag and
// "exact" requires the two sets to be equal.
func matchesTags(recordTags, want []string, mode string) bool {
	if mode == "exact" {
		return sameTagSet(recordTags, want)
	}
	have := make(map[string]struct{}, len(recordTags))
	for _, tag := range recordTags {
		have[tag] = struct{}{}
	}
	requireAll := mode == "all" || mode == "all_strict"
	for _, tag := range want {
		if _, ok := have[tag]; ok {
			if !requireAll {
				return true
			}
			continue
		}
		if requireAll {
			return false
		}
	}
	return requireAll
}

// sameTagSet reports whether two tag lists hold the same members, ignoring
// order and duplicates — the reference's `tags = $2::varchar[]` equality.
func sameTagSet(a, b []string) bool {
	set := make(map[string]struct{}, len(a))
	for _, tag := range a {
		set[tag] = struct{}{}
	}
	for _, tag := range b {
		if _, ok := set[tag]; !ok {
			return false
		}
		delete(set, tag)
	}
	return len(set) == 0
}

// maxTokensFailure validates a supplied max_tokens against the reference's
// bounds, returning nil when it is acceptable.
func maxTokensFailure(value int) *httpx.ValidationFailure {
	if value < minMentalModelTokens {
		return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.GreaterThanEqual([]string{"body", "max_tokens"}, value, minMentalModelTokens),
		}}
	}
	if value > maxMentalModelTokens {
		return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.LessThanEqual([]string{"body", "max_tokens"}, value, maxMentalModelTokens),
		}}
	}
	return nil
}

// decodeBody decodes a JSON body with a size bound, returning the raw object so
// a missing-field item can carry it as input. A body that is absent or the JSON
// literal null is the framework's missing-body item.
func decodeBody(r *http.Request, target any) (map[string]any, *httpx.ValidationFailure) {
	limited := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	defer func() { _ = limited.Close() }()
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, httpx.BodyDecodeFailure(err, body)
	}
	decoded, err := httpx.DecodeLax(body, target)
	httpx.RecordUnknownBodyFields(r, decoded, target)
	trimmed := strings.TrimSpace(string(decoded))
	if trimmed == "" || trimmed == "null" {
		return nil, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.Missing([]string{"body"}, nil),
		}}
	}
	if err != nil {
		return nil, httpx.BodyDecodeFailure(err, decoded)
	}
	object := map[string]any{}
	if err := json.Unmarshal(decoded, &object); err != nil {
		return nil, httpx.BodyDecodeFailure(err, decoded)
	}
	return object, nil
}
