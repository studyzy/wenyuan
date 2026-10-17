package bankapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/storage"
)

// An invalid memory_defense rule is the reference's 422 at the config API (its
// OperationValidationError path), and a valid policy is stored verbatim.
func TestConfigMemoryDefenseValidation(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mustCreate(t, store, storage.Bank{BankID: "b1", Name: "One"})
	handlers := NewHandlers(store)
	handlers.WithConfigAPI(true)
	mux := newMux(handlers)

	cases := []struct {
		name string
		body string
		code int
	}{
		{"unknown action", `{"updates":{"memory_defense":{"enabled":true,"rules":[{"on":"sensitive_data","action":"nope"}]}}}`, http.StatusUnprocessableEntity},
		{"missing on", `{"updates":{"memory_defense":{"enabled":true,"rules":[{"action":"redact"}]}}}`, http.StatusUnprocessableEntity},
		{"empty on", `{"updates":{"memory_defense":{"enabled":true,"rules":[{"on":"","action":"redact"}]}}}`, http.StatusUnprocessableEntity},
		{"valid", `{"updates":{"memory_defense":{"enabled":true,"rules":[{"on":"sensitive_data","action":"block"}]}}}`, http.StatusOK},
	}
	for _, tc := range cases {
		rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config", tc.body)
		if rec.Code != tc.code {
			t.Fatalf("%s: status = %d, want %d, body = %s", tc.name, rec.Code, tc.code, rec.Body.String())
		}
	}

	bank, err := store.GetBank(context.TODO(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := memoryops.ParseDefensePolicy(bank.Config["memory_defense"])
	if err != nil {
		t.Fatalf("stored policy must parse back: %v", err)
	}
	if !policy.Enabled || len(policy.Rules) != 1 || policy.Rules[0].Action != memoryops.DefenseActionBlock {
		t.Fatalf("stored policy = %+v", policy)
	}
}

// The memory_defense policy check keys on the raw field name, exactly as the
// reference's engine pre-check does: an environment-variable-spelled key skips
// the policy validation and is stored verbatim after only the base type check.
func TestConfigMemoryDefenseEnvSpelledKeySkipsPolicyCheck(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mustCreate(t, store, storage.Bank{BankID: "b1", Name: "One"})
	handlers := NewHandlers(store)
	handlers.WithConfigAPI(true)
	mux := newMux(handlers)

	body := `{"updates":{"HINDSIGHT_API_MEMORY_DEFENSE":{"enabled":true,"rules":[{"on":"sensitive_data","action":"nope"}]}}}`
	rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("env-spelled invalid memory_defense = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	bank, err := store.GetBank(context.TODO(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bank.Config["memory_defense"]; !ok {
		t.Fatalf("the normalized key must be stored: %+v", bank.Config)
	}
}
