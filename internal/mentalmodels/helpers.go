package mentalmodels

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// detailParam validates the detail selector, defaulting when absent.
func detailParam(raw, fallback string) string {
	switch strings.TrimSpace(raw) {
	case "metadata", "content", "full":
		return strings.TrimSpace(raw)
	case "":
		return fallback
	default:
		return fallback
	}
}

// pageParams reads limit and offset, defaulting when absent.
func pageParams(r *http.Request, defaultLimit int) (int, int) {
	limit := defaultLimit
	if raw := strings.TrimSpace(httpx.Query(r, "limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	offset := 0
	if raw := strings.TrimSpace(httpx.Query(r, "offset")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	return limit, offset
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
// default mode is any; "all"/"all_strict" require every requested tag.
func matchesTags(recordTags, want []string, mode string) bool {
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

// missingBodyField builds the item the framework emits for an absent required
// body field.
func missingBodyField(field string) *httpx.ValidationFailure {
	return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
		httpx.Missing([]string{"body", field}, map[string]any{}),
	}}
}

// bodyValueError builds the item a custom field validator reports for one body
// field.
func bodyValueError(field string, input any, msg string) *httpx.ValidationFailure {
	return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
		httpx.ValueError([]string{"body", field}, input, msg),
	}}
}

// decodeBody decodes a JSON body with a size bound, reporting a malformed body
// as a request validation failure.
func decodeBody(r *http.Request, target any) *httpx.ValidationFailure {
	limited := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	defer func() { _ = limited.Close() }()
	body, err := io.ReadAll(limited)
	if err != nil {
		return httpx.BodyDecodeFailure(err, body)
	}
	httpx.RecordUnknownBodyFields(r, body, target)
	if err := json.Unmarshal(body, target); err != nil {
		return httpx.BodyDecodeFailure(err, body)
	}
	return nil
}
