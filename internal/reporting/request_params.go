package reporting

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// This file holds the request-reading and error-mapping helpers every route in
// the reporting family shares: query-parameter coercion, the bank existence
// checks, the bounded body reader and the storage-error mapper.

// parseDateParam reads an optional ISO-8601 query parameter, writing a
// bad-request error and reporting false when the value is not a datetime. It
// mirrors the reference's _parse_iso_datetime: date-only and offset-less forms
// are accepted (a naive value is read as UTC), and an absent parameter is
// nothing while a present-but-empty one is a bad request.
func parseDateParam(w http.ResponseWriter, r *http.Request, key string) (*time.Time, bool) {
	if _, present := httpx.QueryParamsOf(r)[key]; !present {
		return nil, true
	}
	raw := httpx.Query(r, key)
	parsed, err := parseISODateTime(raw)
	if err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			fmt.Sprintf("Invalid %s: '%s' is not an ISO-8601 datetime.", key, raw)))
		return nil, false
	}
	utc := parsed.UTC()
	return &utc, true
}

// parseISODateTime parses the ISO-8601 forms Python's datetime.fromisoformat
// accepts: a date-only value, an offset-less datetime (read as UTC), fractional
// seconds, and either a trailing "Z" or a numeric offset. The space separator is
// taken too, which fromisoformat also admits.
func parseISODateTime(value string) (time.Time, error) {
	normalized := value
	if strings.HasSuffix(normalized, "Z") {
		normalized = normalized[:len(normalized)-1] + "+00:00"
	}
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, normalized); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("value is not an ISO-8601 datetime")
}

// strictBoolParam reads a boolean query parameter the way the reference's
// framework coerces one: an absent parameter takes the default, the lax spelling
// set is accepted, and any other value is the framework's bool_parsing failure
// rather than a silent false. It reports whether it produced a value; on false
// the failure has been written.
func strictBoolParam(w http.ResponseWriter, r *http.Request, name string, def bool) (bool, bool) {
	values, present := httpx.QueryPresent(r, name)
	if !present || len(values) == 0 {
		return def, true
	}
	// The reference binds the LAST occurrence of a repeated scalar parameter.
	raw := values[len(values)-1]
	switch strings.ToLower(raw) {
	case "true", "1", "yes", "on", "t", "y":
		return true, true
	case "false", "0", "no", "off", "f", "n":
		return false, true
	}
	httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
		httpx.TypeMismatch("bool_parsing", []string{"query", name}, raw),
	}})
	return false, false
}

// requireBank reports not-found for an unknown bank.
//
// A bank may be missing and still be readable: a team member whose own bank has
// not been created yet is served their project's shared content, which is the
// onboarding path. So a missing bank is only refused when the deployment has no
// team scope to serve it from.
func (h *Handlers) requireBank(w http.ResponseWriter, r *http.Request) (string, bool) {
	bankID := PathBankID(r)
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return "", false
	}
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		if h.TeamScope == nil || !isMissingBank(err) {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
			return "", false
		}
		// A deployment with teams may still have content for this caller. The
		// read itself decides whether it does: an unresolved scope means the
		// caller is not a member of anything, and the store then answers the
		// same not-found through its own lookup.
		if h.teamFallback(r.Context(), bankID) == nil {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
			return "", false
		}
	}
	return bankID, true
}

// isMissingBank reports whether an error is the store's "no such bank".
func isMissingBank(err error) bool {
	return errors.Is(err, storage.ErrBankNotFound)
}

// requireOrCreateBank resolves the bank, creating it lazily when absent — the
// reference's knowledge-node create paths do this, so a page can be the first
// thing a bank holds without the caller creating the bank beforehand.
func (h *Handlers) requireOrCreateBank(w http.ResponseWriter, r *http.Request) (string, bool) {
	bankID := PathBankID(r)
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return "", false
	}
	if err := h.Store.EnsureBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return "", false
	}
	return bankID, true
}

// boundedIntParam reads an optional integer query parameter with an inclusive
// [min,max] bound (a negative max means unbounded above), defaulting when absent.
// A malformed, non-integer, or out-of-range value is rejected with the
// framework's 422 validation envelope before the handler body runs, matching how
// FastAPI validates a typed Query before the route function.
func boundedIntParam(w http.ResponseWriter, r *http.Request, name string, def, min, max int) (int, bool) {
	raw := httpx.Query(r, name)
	if raw == "" {
		return def, true
	}
	// A negative max means unbounded above, which the shared reader spells 0.
	bound := max
	if max < 0 {
		bound = 0
	}
	value, item, ok := httpx.ParseQueryInt(raw, name, min, bound)
	if !ok {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{item}})
		return 0, false
	}
	return value, true
}

// readOptionalBody reads a request body with a size bound, returning the raw
// bytes (possibly empty) so a caller can tell an absent body from a present one.
func readOptionalBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	limited := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	defer func() { _ = limited.Close() }()
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("request body must be JSON: %w", err)
	}
	return body, nil
}

// writeRequiredBodyFailure emits the framework's 422 body for a missing or
// malformed required JSON body.
func writeRequiredBodyFailure(w http.ResponseWriter, r *http.Request, err error) {
	httpx.WriteValidationFailure(w, httpx.BodyDecodeFailure(err, nil))
}

// enumParam reads an optional parameter declared as a `Literal` enum in the
// reference, reporting whether it settled on a value. An absent parameter is
// empty; a value outside allowed is the framework's literal_error 422.
//
// The framework resolves the typed query members before the handler body runs,
// so the caller must validate before its own first act — the bank lookup.
func enumParam(w http.ResponseWriter, r *http.Request, name string, allowed []string) (string, bool) {
	values, present := httpx.QueryPresent(r, name)
	if !present || len(values) == 0 {
		return "", true
	}
	// The reference binds the LAST occurrence of a repeated scalar parameter.
	raw := values[len(values)-1]
	for _, token := range allowed {
		if token == raw {
			return raw, true
		}
	}
	writeQueryEnumFailure(w, r, name, raw, allowed)
	return "", false
}

// writeQueryEnumFailure emits the framework's 422 body for a query parameter
// whose value is outside its declared enum. Both callers (tags_match,
// time_field) are declared as `Literal` in the reference, whose rejected-value
// item is `literal_error` rather than `enum`, so the kind is emitted as such.
func writeQueryEnumFailure(w http.ResponseWriter, r *http.Request, name, input string, allowed []string) {
	httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
		httpx.Literal([]string{"query", name}, input, allowed),
	}})
}

// asHTTPError maps a storage error to the shared envelope.
func asHTTPError(err error) *httpx.HTTPError {
	switch {
	case err == nil:
		return nil
	case isHTTPError(err):
		var httpErr *httpx.HTTPError
		_ = errors.As(err, &httpErr)
		return httpErr
	case errors.Is(err, storage.ErrBankNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank)
	case errors.Is(err, storage.ErrMemoryNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "memory not found")
	case errors.Is(err, storage.ErrKnowledgeNodeNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "knowledge node not found")
	case errors.Is(err, storage.ErrAuditEntryNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "audit entry not found")
	case errors.Is(err, storage.ErrMentalModelNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "mental model not found")
	case errors.Is(err, storage.ErrInvalidMentalModelSubtype):
		return httpx.NewHTTPError(httpx.KindBadRequest, "unknown mental model subtype")
	case errors.Is(err, storage.ErrEmbeddingDimensions):
		return httpx.NewHTTPError(httpx.KindBadRequest, err.Error())
	case errors.Is(err, storage.ErrInvalidBankID):
		return httpx.NewHTTPError(httpx.KindBadRequest, "bank identifier is invalid")
	case errors.Is(err, storage.ErrAliasConflict):
		return httpx.NewHTTPError(httpx.KindConflict, "name conflicts with an existing bank or alias")
	case errors.Is(err, storage.ErrWritesUnavailable):
		return httpx.NewHTTPError(httpx.KindUnavailable, httpx.DetailWritesDown)
	default:
		// Anything the storage layer did not recognise is a server fault, not a
		// malformed request: the reference's catch-all answers 500 and reports the
		// error it caught.
		return httpx.NewHTTPError(httpx.KindInternal, err.Error())
	}
}

// isHTTPError reports whether the error already carries a status and detail.
func isHTTPError(err error) bool {
	var target *httpx.HTTPError
	return errors.As(err, &target)
}

// newID builds an opaque identifier.
func newID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("op-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}
