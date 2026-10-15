package app

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The bank identifier is an unconstrained string on the read surface: the
// reference's OpenAPI declares it `type: string` with no length or pattern, so
// a route that does not create a bank passes whatever it is handed straight to
// the store, which answers with its own missing-bank result. Only the create
// path constrains a *new* bank id — empty, over 192 UTF-8 bytes, or containing
// a control character is refused with 400 — and it applies that rule to the
// implicit create a first retain performs as well as to the explicit one
// (engine/retain/bank_utils.py:validate_new_bank_id). Existing banks are never
// re-checked.
//
// The reference's own blackbox story asserts both halves:
//   - test_50_banks_and_config.py::test_a_bank_id_over_the_byte_limit_is_refused_before_the_bank_exists
//     (400 from acreate_bank and from aretain)
//   - test_50_banks_and_config.py::test_a_bank_that_does_not_exist_is_a_404_not_an_empty_answer
//     (404 from an unknown-bank read)
//
// The bank id reaches SQL only as a bound parameter ($1), never interpolated, so
// no shape — over-long or packed with path-legal oddities — needs a guard for
// SQL safety. Go's ServeMux also imposes no path-segment length limit, so the
// over-long shape below reaches the handler rather than being rejected by the
// router; the 192-byte cap is the only length bound and it belongs to the
// create path, as the reference has it.
func TestBankIDShapeThroughServedHandler(t *testing.T) {
	overlong := "systest-" + strings.Repeat("中", 62) // 8 + 186 = 194 UTF-8 bytes
	malformed := "bad\x01id"                         // a control character (Cc)
	nonexistent := "systest-no-such-bank"

	// Creation routes refuse a malformed or over-long identifier with 400,
	// before the bank exists, and accept a well-formed new one.
	creation := []struct {
		name, method, suffix, body string
	}{
		{"create bank", http.MethodPut, "", `{"name":"x"}`},
		{"retain (sync)", http.MethodPost, "/memories", `{"items":[{"content":"Alice moved to Berlin."}]}`},
		{"retain (async)", http.MethodPost, "/memories", `{"items":[{"content":"Alice moved to Berlin."}],"async":true}`},
		{"patch config", http.MethodPatch, "/config", `{"updates":{"reflect_mission":"be careful"}}`},
	}
	for _, route := range creation {
		for _, id := range []string{overlong, malformed} {
			app := newParityApp(t)
			rec := send(app, route.method, bankPath(id, route.suffix), route.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s with a refused id (%d bytes): status = %d, want 400, body = %s",
					route.name, len(id), rec.Code, rec.Body.String())
			}
		}
		// A well-formed id that does not exist yet is created, not refused.
		app := newParityApp(t)
		rec := send(app, route.method, bankPath(nonexistent, route.suffix), route.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s into a fresh bank: status = %d, want 200, body = %s", route.name, rec.Code, rec.Body.String())
		}
	}

	// Read routes carry no shape check: a malformed, over-long or merely absent
	// id is the store's missing-bank answer, and the detail names the bank the
	// caller asked for.
	reads := []struct {
		name, method, suffix string
	}{
		{"update bank", http.MethodPatch, ""},
		{"read config", http.MethodGet, "/config"},
	}
	for _, route := range reads {
		for _, id := range []string{overlong, malformed, nonexistent} {
			app := newParityApp(t)
			rec := send(app, route.method, bankPath(id, route.suffix), `{}`)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s for id %q: status = %d, want the store's 404, body = %s",
					route.name, id, rec.Code, rec.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s for id %q: body is not JSON: %v", route.name, id, err)
			}
			detail, _ := body["detail"].(string)
			if detail != "Bank '"+id+"' not found" {
				t.Fatalf("%s for id %q: detail = %q, want the store's named missing-bank answer", route.name, id, detail)
			}
		}
	}
}

// bankPath builds a bank-scoped path with the identifier percent-encoded, so a
// control character or a multibyte id survives the request line intact.
func bankPath(id, suffix string) string {
	return "/v1/default/banks/" + url.PathEscape(id) + suffix
}
