package bankapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// The reference validates an entity_labels write with its model and answers the
// model's report, so a value the model rejects is a 400 with that report. A
// loose shape check would accept these and store them.
func TestUpdateBankConfigRejectsAnInvalidEntityLabelsValue(t *testing.T) {
	cases := []struct{ name, updates string }{
		{"a non-string key", `{"entity_labels":{"attributes":[{"key":1}]}}`},
		{"a null type", `{"entity_labels":{"attributes":[{"key":"k","type":null}]}}`},
		{"a null values list", `{"entity_labels":{"attributes":[{"key":"k","values":null}]}}`},
		{"a non-string value description", `{"entity_labels":{"attributes":[{"key":"k","values":[{"value":"v","description":1}]}]}}`},
		{"a non-boolean optional", `{"entity_labels":{"attributes":[{"key":"k","optional":"x"}]}}`},
		{"a non-boolean tag", `{"entity_labels":{"attributes":[{"key":"k","tag":"x"}]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := storage.NewMemoryStore("public")
			mustCreate(t, store, storage.Bank{BankID: "b1", Name: "One"})
			mux := newMux(newConfigHandler(store))

			rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config", `{"updates":`+tc.updates+`}`)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
			var envelope struct {
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if prefix := "Invalid entity_labels format: "; len(envelope.Detail) <= len(prefix) ||
				envelope.Detail[:len(prefix)] != prefix {
				t.Fatalf("detail = %q, want the model's report", envelope.Detail)
			}
			// Nothing is stored: the write is rejected whole.
			bank, err := store.GetBank(context.TODO(), "b1")
			if err != nil {
				t.Fatal(err)
			}
			if _, present := bank.Config["entity_labels"]; present {
				t.Fatalf("a rejected value must not be stored: %+v", bank.Config)
			}
		})
	}
}

// The stored value is the caller's own dict in the reference's serialiser
// order, so a read-back matches what the reference returns. Storing the
// caller's bytes verbatim, or sorting the members, changes it.
func TestUpdateBankConfigStoresTheCanonicalEntityLabelsValue(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mustCreate(t, store, storage.Bank{BankID: "b1", Name: "One"})
	mux := newMux(newConfigHandler(store))

	rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config",
		`{"updates":{"entity_labels":{"attributes":[{"optional":false,"tag":true,`+
			`"values":[{"description":"d","value":"a"}],"key":"k","type":"value"}]}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", rec.Code, rec.Body.String())
	}
	// The read-back is what a client compares, and it is where the jsonb
	// column's lost member order has to be re-stated.
	want := `{"attributes":[{"key":"k","type":"value","optional":false,"tag":true,` +
		`"values":[{"value":"a","description":"d"}]}]}`
	read := doJSON(t, mux, http.MethodGet, "/v1/default/banks/b1/config", "")
	if read.Code != http.StatusOK {
		t.Fatalf("read: %d", read.Code)
	}
	var response struct {
		Overrides map[string]json.RawMessage `json:"overrides"`
	}
	if err := json.Unmarshal(read.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	stored, ok := response.Overrides["entity_labels"]
	if !ok {
		t.Fatalf("overrides = %s", read.Body.String())
	}
	if string(stored) != want {
		t.Fatalf("read-back value =\n  %s\nwant\n  %s", stored, want)
	}

	// A group that names only its key stays that way: the legacy migration's
	// `type` exists to validate, not to store.
	if rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config",
		`{"updates":{"entity_labels":{"attributes":[{"key":"only"}]}}}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", rec.Code, rec.Body.String())
	}
	bank, err := store.GetBank(context.TODO(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(bank.Config["entity_labels"])
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"attributes":[{"key":"only"}]}` {
		t.Fatalf("stored value = %s", encoded)
	}
}
