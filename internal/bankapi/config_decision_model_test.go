package bankapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// decisionModelKeys are the four v1.1.0 decision-orchestration switches: the
// reference declares each a configurable field (config.py:4091-4163) resolved
// true by default (config.py:1889, 2084-2087), so a bank reads true before it
// writes anything and can store its own bool or clear the override with null.
var decisionModelKeys = []string{
	"reflect_use_decision_model",
	"mental_model_refresh_use_decision_model",
	"mental_model_delta_use_decision_model",
	"consolidation_use_decision_model",
}

// The resolved read carries every decision-model switch at its default, which
// is on, whether or not the bank overrode it.
func TestBankConfigResolvesTheDecisionModelSwitches(t *testing.T) {
	mux, _ := configServer(t)

	rec := doJSON(t, mux, http.MethodGet, "/v1/default/banks/b1/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	body := decodeConfig(t, rec.Body.Bytes())
	for _, key := range decisionModelKeys {
		if value, present := body.Config[key]; !present || value != true {
			t.Fatalf("config[%q] = %#v (present %v), want the default true", key, value, present)
		}
		if _, present := body.Overrides[key]; present {
			t.Fatalf("overrides[%q] must be absent before the bank writes it", key)
		}
	}
}

// A write of each switch is stored verbatim and echoed by the read, and a null
// clears the stored override so the read falls back to the default.
func TestBankConfigWritesTheDecisionModelSwitches(t *testing.T) {
	mux, store := configServer(t)

	for _, key := range decisionModelKeys {
		rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config",
			`{"updates":{"`+key+`":false}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body %s", key, rec.Code, rec.Body.String())
		}
		bank, err := store.GetBank(t.Context(), "b1")
		if err != nil {
			t.Fatal(err)
		}
		if bank.Config[key] != false {
			t.Fatalf("stored %s = %#v, want false", key, bank.Config[key])
		}
		body := decodeConfig(t, rec.Body.Bytes())
		if value := body.Overrides[key]; value != false {
			t.Fatalf("overrides[%q] = %#v, want the stored false", key, body.Overrides[key])
		}
		if value := body.Config[key]; value != false {
			t.Fatalf("config[%q] = %#v, want the override false", key, body.Config[key])
		}

		// Null is the reference's tombstone: the stored override is dropped, so
		// the read resolves the shipped default again.
		rec = doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config",
			`{"updates":{"`+key+`":null}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s null: status = %d, body %s", key, rec.Code, rec.Body.String())
		}
		bank, err = store.GetBank(t.Context(), "b1")
		if err != nil {
			t.Fatal(err)
		}
		if _, present := bank.Config[key]; present {
			t.Fatalf("stored %s = %#v, want the override cleared", key, bank.Config[key])
		}
		body = decodeConfig(t, rec.Body.Bytes())
		if value, present := body.Config[key]; !present || value != true {
			t.Fatalf("config[%q] = %#v (present %v), want the default true after the clear", key, value, present)
		}
		if _, present := body.Overrides[key]; present {
			t.Fatalf("overrides[%q] must be absent after the clear", key)
		}
	}
}

// A non-boolean value is refused with the field's own type message, as every
// kindBool field is.
func TestBankConfigRefusesWrongTypedDecisionModelSwitches(t *testing.T) {
	mux, _ := configServer(t)
	for _, key := range decisionModelKeys {
		rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config",
			`{"updates":{"`+key+`":"yes"}}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (body %s)", key, rec.Code, rec.Body.String())
		}
		want := key + " must be a boolean, got str"
		if got := rec.Body.String(); !strings.Contains(got, want) {
			t.Fatalf("%s: body = %s, want it to contain %q", key, got, want)
		}
	}
	var _ storage.Bank
}
