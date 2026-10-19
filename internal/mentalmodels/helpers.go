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
// only absence falls back to the default. A parameter that is present but empty
// (`?detail=`) is a value, not an absence: the framework binds the empty string
// and the Literal rejects it, so it does not take the default either.
func detailParam(w http.ResponseWriter, r *http.Request, fallback string) (string, bool) {
	raw, present := scalarQuery(r, "detail")
	if !present {
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
// Like detailParam, a present-but-empty value is rejected, not defaulted.
func tagsMatchParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw, present := scalarQuery(r, "tags_match")
	if !present {
		return "any", true
	}
	if !contains(tagsMatchValues, raw) {
		writeQueryItem(w, httpx.Literal([]string{"query", "tags_match"}, raw, tagsMatchValues))
		return "", false
	}
	return raw, true
}

// scalarQuery reads a declared scalar query parameter and reports whether its
// key was present at all, returning the value exactly as sent. The reference
// binds a declared parameter whenever its key appears — `?detail=` binds the
// empty string, which its Literal then refuses — and the framework does not
// trim the text before matching it, so neither does this read.
func scalarQuery(r *http.Request, name string) (string, bool) {
	values, present := httpx.QueryPresent(r, name)
	if !present || len(values) == 0 {
		return "", false
	}
	return values[len(values)-1], true
}

// pageParams reads limit and offset with the reference's bounds: limit is
// 1..1000 defaulting to 100, offset is >= 0 with no upper bound, defaulting to
// 0. A malformed or out-of-range value writes the 422 envelope and reports
// false.
func pageParams(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	limit, ok := boundedQueryInt(w, r, "limit", 100, 1, 1000)
	if !ok {
		return 0, 0, false
	}
	// offset declares ge=0 and no le, so a max below min here means "no upper
	// bound" — an offset past the end is an empty page, not a 422.
	offset, ok := boundedQueryInt(w, r, "offset", 0, 0, -1)
	if !ok {
		return 0, 0, false
	}
	return limit, offset, true
}

// boundedQueryInt reads an optional integer query parameter. Absence yields def;
// a parameter that is present but empty, a malformed value, or a value outside
// the bound is the framework's 422. A max below min means no upper bound.
func boundedQueryInt(w http.ResponseWriter, r *http.Request, name string, def, min, max int) (int, bool) {
	raw, present := scalarQuery(r, name)
	if !present {
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

// maxTokensFailureFor is maxTokensFailure with the item's `input` taken from the
// value the CLIENT sent, which is what the framework echoes. The lax coercion
// accepts a boolean for an integer member (true reads as 1), so a value that is
// in range after coercion can still be one the bound should name in its own
// terms; and a value that is out of range must be echoed as sent, not as the
// number it coerced to.
func maxTokensFailureFor(value int, sent any) *httpx.ValidationFailure {
	if value < minMentalModelTokens {
		return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.GreaterThanEqual([]string{"body", "max_tokens"}, sent, minMentalModelTokens),
		}}
	}
	if value > maxMentalModelTokens {
		return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.LessThanEqual([]string{"body", "max_tokens"}, sent, maxMentalModelTokens),
		}}
	}
	return nil
}

// maxTokensItems is the item a max_tokens value outside the model's range
// reports, or nil when it is in range.
func maxTokensItems(value *int, sent any) []httpx.ValidationError {
	if value == nil {
		return nil
	}
	if failure := maxTokensFailureFor(*value, sent); failure != nil {
		return failure.Errors
	}
	return nil
}

// insertBeforeLoc places items ahead of the first item whose location starts
// with loc, so a check the contract cannot express lands where the model
// declares the member rather than at the end of the list.
func insertBeforeLoc(items []httpx.ValidationError, loc []string, extra []httpx.ValidationError) []httpx.ValidationError {
	if len(extra) == 0 {
		return items
	}
	for index, item := range items {
		if hasLocPrefix(item.Loc, loc) {
			out := make([]httpx.ValidationError, 0, len(items)+len(extra))
			out = append(out, items[:index]...)
			out = append(out, extra...)
			return append(out, items[index:]...)
		}
	}
	return append(items, extra...)
}

// hasLocPrefix reports whether loc begins with prefix.
func hasLocPrefix(loc, prefix []string) bool {
	if len(loc) < len(prefix) {
		return false
	}
	for i := range prefix {
		if loc[i] != prefix[i] {
			return false
		}
	}
	return true
}

// requestItems reads the body and returns it with every item the request model's
// contract files — one per failing member, in the model's declaration order. The
// reference's model reports them all in one envelope, so a decoder that stopped
// at the first failing member would hide the rest; this reports them beside the
// items a route derives itself.
func requestItems(r *http.Request, target any) (map[string]any, []httpx.ValidationError, *httpx.ValidationFailure) {
	limited := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	defer func() { _ = limited.Close() }()
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, nil, httpx.BodyDecodeFailure(err, body)
	}
	if trimmed := strings.TrimSpace(string(body)); trimmed == "" || trimmed == "null" {
		return nil, nil, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.Missing([]string{"body"}, nil),
		}}
	}
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		return nil, nil, httpx.BodyDecodeFailure(err, body)
	}
	// The trigger's items come from the trigger walk, which knows the custom
	// validators the request model's contract cannot express, so the shared
	// walk's items for it are dropped here.
	var items []httpx.ValidationError
	for _, item := range httpx.ValidationItemsAgainstRequest(r, target, body) {
		if triggerLocBelow(item.Loc) {
			continue
		}
		items = append(items, item)
	}
	return object, items, nil
}

// triggerLocBelow reports whether an item is inside the trigger member.
func triggerLocBelow(loc []string) bool {
	return len(loc) > 1 && loc[1] == "trigger"
}

// decodeInto decodes the already-read body object into the request struct. The
// object came from the same body, and every member whose JSON kind does not fit
// the struct is one the shared walk has already filed as an item — so a decode
// failure here is expected and carries nothing the caller has not been told.
// The refused members are left at their zero value.
func decodeInto(object map[string]any, target any) {
	raw, err := json.Marshal(object)
	if err == nil {
		_ = json.Unmarshal(raw, target)
	}
}
