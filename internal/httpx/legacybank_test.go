package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// A bank whose id carries a prefix that is not the caller's tenant is refused
// bank-scoped — except that a whole-bank delete or replace of an id that really
// is a bank of the caller's own schema is let through.
//
// The case this exists for: earlier clients wrote `public::<name>` banks into a
// tenant's schema, so the console lists them under that tenant while the prefix
// rule refuses every request that names them. An operator has to be able to
// delete one from the console rather than from SQL. What must not come back is
// the hole the prefix rule closed: reading, listing, or touching a sub-resource
// of a foreign-prefixed id stays the missing-bank 404, and a bank that lives in
// another schema is not the caller's even for a delete.
func TestLegacyPrefixedBankIsDeletableButNotReadable(t *testing.T) {
	extension := &stubTenantExtension{contexts: map[string]tenant.Context{
		"acme": {ID: "acme", Schema: "acme"},
		"bob":  {ID: "bob", Schema: "bob"},
	}}
	// The banks each schema really holds. The hook is asked with the schema the
	// request resolved to, so "bob::thing" is absent from acme's answer.
	banks := map[string]map[string]bool{
		"acme": {"acme::mine": true, "public::legacy": true},
		"bob":  {"bob::thing": true},
	}
	exists := func(ctx context.Context, bankID string) (bool, error) {
		schema, ok := storage.SchemaFrom(ctx)
		if !ok {
			return false, nil
		}
		return banks[schema][bankID], nil
	}
	router := NewRouter(RouterOptions{TenantExtension: extension, BankExists: exists})
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	router.Handle(http.MethodGet, BankMemberPath(), ok)
	router.Handle(http.MethodDelete, BankMemberPath(), ok)
	router.Handle(http.MethodPut, BankMemberPath(), ok)
	router.Handle(http.MethodPatch, BankMemberPath(), ok)
	router.Handle(http.MethodGet, BankPath("/config"), ok)

	cases := []struct {
		name, credential, method, target string
		want                             int
	}{
		{"delete a legacy bank of my schema", "acme", http.MethodDelete,
			"/v1/default/banks/public::legacy", http.StatusOK},
		{"replace a legacy bank of my schema", "acme", http.MethodPut,
			"/v1/default/banks/public::legacy", http.StatusOK},
		{"reading a legacy bank stays refused", "acme", http.MethodGet,
			"/v1/default/banks/public::legacy", http.StatusNotFound},
		{"a legacy bank's sub-resource stays refused", "acme", http.MethodGet,
			"/v1/default/banks/public::legacy/config", http.StatusNotFound},
		{"patching a legacy bank stays refused", "acme", http.MethodPatch,
			"/v1/default/banks/public::legacy", http.StatusNotFound},
		{"deleting an id my schema does not hold", "acme", http.MethodDelete,
			"/v1/default/banks/ghost::legacy", http.StatusNotFound},
		{"deleting another tenant's bank", "acme", http.MethodDelete,
			"/v1/default/banks/bob::thing", http.StatusNotFound},
		{"my own bank still deletes", "acme", http.MethodDelete,
			"/v1/default/banks/acme::mine", http.StatusOK},
		{"my own bank still reads", "acme", http.MethodGet,
			"/v1/default/banks/acme::mine", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.target, nil)
			request.Header.Set("Authorization", "Bearer "+tc.credential)
			recorder := httptest.NewRecorder()
			router.Service().ServeHTTP(recorder, request)
			if recorder.Code != tc.want {
				t.Fatalf("%s %s answered %d, want %d: %s",
					tc.method, tc.target, recorder.Code, tc.want, recorder.Body.String())
			}
		})
	}
}

// With no existence hook wired the prefix rule is strict: nothing is guessed
// from the id alone.
func TestLegacyPrefixedBankNeedsTheExistenceHook(t *testing.T) {
	extension := &stubTenantExtension{contexts: map[string]tenant.Context{
		"acme": {ID: "acme", Schema: "acme"},
	}}
	router := NewRouter(RouterOptions{TenantExtension: extension})
	router.Handle(http.MethodDelete, BankMemberPath(),
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	request := httptest.NewRequest(http.MethodDelete, "/v1/default/banks/public::legacy", nil)
	request.Header.Set("Authorization", "Bearer acme")
	recorder := httptest.NewRecorder()
	router.Service().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", recorder.Code, recorder.Body.String())
	}
}
