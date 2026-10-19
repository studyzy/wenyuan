package directives

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// directive_uuid.go reproduces the failure a directive route reports when the
// path's id is not a UUID.
//
// Every directive query binds the id to a `uuid` column (`directives.id` is
// `uuid` in the reference schema). The reference passes the raw path string as
// a query argument and never converts it, so the failure surfaces from the
// driver while it binds the argument — not from the database when it executes.
// The driver therefore answers with its own argument message, and the reference
// turns that into a 500 whose detail is the driver's text:
//
//	invalid input for query argument $2: 'not-a-uuid' (invalid UUID 'not-a-uuid':
//	length must be between 32..36 characters, got 10)
//
// That leaking driver text is part of the contract, so it is reproduced here
// rather than tidied into a 400 or a 404. Two details are load-bearing:
//
//   - The value appears twice. The outer copy is truncated to 32 characters and
//     marked with "..."; the inner copy is the whole value. Both are quoted.
//   - The argument number is not always $2: the driver numbers the parameters
//     in the order the reference binds them, so on PATCH — whose UPDATE puts
//     name, content, priority, is_active and tags ahead of the id — the id is
//     $2 plus however many of those five the body supplied.
//
// The id is checked exactly as the driver checks it, in the same order: length
// first, then that every character is a hex digit or a hyphen, then that the
// remaining hex digits fill sixteen bytes. Hyphens are skipped wherever they
// appear, so "01234567-89abcdef0123456789abcdef" is a valid UUID even though
// the hyphens are not in the canonical positions.

// uuidMember is the resolver the id-bearing routes share: it reports the
// driver's argument failure for a segment that is not a UUID before the store
// is touched.
// uuidMember resolves the id the way the routes that put no other bind
// parameters ahead of it report it: GET and DELETE bind the id as the query's
// second argument.
func (h *Handlers) uuidMember(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	bankID, id, ok := h.member(w, r)
	if !ok {
		return "", "", false
	}
	if detail, bad := directiveUUIDReport(id, 2); bad {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, detail))
		return "", "", false
	}
	return bankID, id, true
}

// directiveUUIDReport renders the driver's argument-binding failure for value,
// bound as query parameter argNumber, or reports false when value is a UUID the
// driver accepts.
func directiveUUIDReport(value string, argNumber int) (string, bool) {
	hexDigits := 0
	invalid := byte(0)
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c == '-':
		case isHexDigit(c):
			hexDigits++
		case invalid == 0:
			invalid = c
		}
	}

	var reason string
	switch {
	case len(value) < 32 || len(value) > 36:
		reason = fmt.Sprintf("length must be between 32..36 characters, got %d", len(value))
	case invalid != 0:
		reason = "unexpected character " + quoteRune(invalid)
	case hexDigits < 32:
		reason = "decodes to less than 16 bytes"
	case hexDigits > 32:
		reason = "decodes to more than 16 bytes"
	default:
		// A UUID the driver accepts; an absent row is the route's own 404.
		return "", false
	}

	if len(value) > 38 {
		// A long value is printed with a bounded width: the driver keeps the
		// first 39 characters, drops the closing quote and appends "...", so the
		// truncated copy reads 'aaaaaa... (invalid UUID ...
		// A value of 38 characters or fewer is printed whole.
		return fmt.Sprintf("invalid input for query argument $%d: '%s... (invalid UUID '%s': %s)",
			argNumber, value[:39], value, reason), true
	}
	return fmt.Sprintf("invalid input for query argument $%d: '%s' (invalid UUID '%s': %s)",
		argNumber, value, value, reason), true
}

// isHexDigit reports whether c is an ASCII hexadecimal digit.
func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// quoteRune renders c the way the driver's message does: in single quotes, with
// a control character or a quote shown as a backslash escape.
func quoteRune(c byte) string {
	switch c {
	case '\'':
		return `"\'"`
	case '\\':
		return `"\\"`
	case '\n':
		return `"\n"`
	case '\r':
		return `"\r"`
	case '\t':
		return `"\t"`
	}
	if c < 0x20 || c == 0x7f {
		return fmt.Sprintf(`"\x%02x"`, c)
	}
	return "'" + string(rune(c)) + "'"
}

// updateDirectiveArgNumber returns the query-parameter position the driver
// gives the id on a PATCH, which follows the fields the body actually supplied.
//
// The reference builds its UPDATE by appending `name = $1, content = $2,
// priority = $3, is_active = $4, tags = $5` for each member that is not None,
// then binds bank_id and the id last. A member counts as supplied unless the
// body omitted it or set it to null, which is why `{}` and a fully populated
// body number the id differently.
func updateDirectiveArgNumber(supplied [5]bool) int {
	position := 2
	for _, present := range supplied {
		if present {
			position++
		}
	}
	return position
}

// foreignKeyReport restates a failed directive insert against an unknown bank
// in the shape the reference reports it.
//
// The reference never checks that the bank exists on this route: it issues the
// INSERT and lets `fk_directives_bank_id` reject an unknown bank. The error
// reaches the caller as `str(e)`, which is the driver's message followed by a
// DETAIL line naming the rejected key — and *without* the ERROR: prefix and
// SQLSTATE suffix that this service's driver spells around the same message.
//
// The driver's message ends at the constraint name; it carries neither the key
// nor a DETAIL line, so the key is supplied by the caller, which is the one
// place that knows what it tried to insert.
func foreignKeyReport(err error, bankID string) (string, bool) {
	if err == nil {
		return "", false
	}
	text := err.Error()
	const marker = `violates foreign key constraint "`
	at := strings.Index(text, marker)
	if at < 0 {
		return "", false
	}
	rest := text[at+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return "", false
	}
	if constraint := rest[:end]; constraint != "fk_directives_bank_id" {
		return "", false
	}

	// Keep the driver's whole sentence through the constraint name, dropping only
	// the ERROR: prefix and the SQLSTATE suffix it wraps around it, then add the
	// DETAIL line the reference prints underneath. The two spaces after
	// "DETAIL:" are the driver's.
	message := strings.TrimPrefix(text[:at+len(marker)+end+1], "ERROR: ")
	message = strings.TrimSpace(message)
	return fmt.Sprintf("%s\nDETAIL:  Key (bank_id)=(%s) is not present in table \"banks\".", message, bankID), true
}
